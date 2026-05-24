// Command loadtest is the Go port of Night.Ms.Tools.LoadTest — a synthetic
// SSH load harness used to verify "200 concurrent sessions, 10 minutes,
// stable RSS".
//
// Bots drive the TUI blindly through stdin (no screen-scrape). Scenarios:
//
//	idle    — hold a lobby session for the whole window. Stresses connect
//	          / handshake / Redis presence heartbeat / steady-state RSS.
//	chat    — enter Chat once, then post a short message every 8–20s. The
//	          Redis pub/sub fan-out delivers each message to every other
//	          chat-scenario bot subscribed to #lobby; this is the realtime
//	          path most likely to break under load.
//	forums  — every 10–25s: open Boards, drill into the first forum and
//	          first topic, then Esc back to the lobby. Hammers the
//	          forums.sql read path (forums list + topics list + posts
//	          window + post_reads marker write).
//	mix     — per-bot 70/30 split between chat and forums. Default.
//
// Subcommands:
//
//	loadtest seed -count N [-password P]
//	loadtest run  -count N [-host h:p] [-ramp 30s] [-duration 5m]
//	              [-scenario idle|chat|forums|mix] [-password P]
//	loadtest clean
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"os"
	"os/signal"
	"sort"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	gossh "golang.org/x/crypto/ssh"

	"github.com/nickna/ssh.night.ms/internal/auth"
	"github.com/nickna/ssh.night.ms/internal/config"
)

const handlePrefix = "loadbot-"

func main() {
	if len(os.Args) < 2 {
		usage(2)
	}
	sub := os.Args[1]
	args := os.Args[2:]
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	switch sub {
	case "seed":
		os.Exit(runSeed(ctx, args, logger))
	case "run":
		os.Exit(runRun(ctx, args, logger))
	case "clean":
		os.Exit(runClean(ctx, logger))
	case "-h", "--help", "help":
		usage(0)
	default:
		fmt.Fprintln(os.Stderr, "loadtest: unknown subcommand", sub)
		usage(2)
	}
}

func usage(code int) {
	fmt.Fprintln(os.Stderr, `loadtest — synthetic SSH load harness for ssh.night.ms

  loadtest seed -count N [-password P]
      Insert N loadbot-NNNN users with the given password. Idempotent.
  loadtest run -count N [-host HOST:PORT] [-ramp DURATION] [-duration DURATION]
               [-scenario idle|chat|forums|mix] [-password P]
      Hold N concurrent SSH sessions for -duration, ramped over -ramp. Each
      session runs the chosen scenario. Reports session latency + per-
      scenario action counts.
  loadtest clean
      Delete all loadbot-* users (cascades their credentials).

Default password: loadtest-2026
Default scenario: mix`)
	os.Exit(code)
}

// scenarioName picks the action pattern a bot follows for the duration of
// its session. See the package doc for what each one does.
type scenarioName string

const (
	scenarioIdle   scenarioName = "idle"
	scenarioChat   scenarioName = "chat"
	scenarioForums scenarioName = "forums"
	scenarioMix    scenarioName = "mix"
)

// counters accumulate per-action totals across all bots. Reported in the
// final summary so a 0-failure run with 0 chat sends is visibly different
// from a 0-failure run with 8000 chat sends.
type counters struct {
	chatSends   atomic.Int64
	forumVisits atomic.Int64
}

var globalCounters counters

// sleepCtx waits for d or ctx cancellation. Returns true if the full
// duration elapsed; false if ctx cancelled first. Scenarios use the bool
// to bail out of their loop early.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func runSeed(ctx context.Context, args []string, logger *slog.Logger) int {
	fs := flag.NewFlagSet("seed", flag.ExitOnError)
	count := fs.Int("count", 0, "number of bots to seed")
	password := fs.String("password", "loadtest-2026", "shared bot password")
	_ = fs.Parse(args)
	if *count <= 0 {
		fmt.Fprintln(os.Stderr, "seed: -count required")
		return 2
	}

	pool, err := openPool(ctx)
	if err != nil {
		logger.Error("pool", "err", err)
		return 1
	}
	defer pool.Close()

	hasher := auth.NewHasher(auth.DefaultArgon2Params())
	hash, algo, err := hasher.Hash(*password)
	if err != nil {
		logger.Error("hash", "err", err)
		return 1
	}
	var algoPtr *string
	if algo != "" {
		algoPtr = &algo
	}
	now := time.Now().UTC()

	created := 0
	skipped := 0
	for i := 1; i <= *count; i++ {
		handle := fmt.Sprintf("%s%04d", handlePrefix, i)
		var existing int64
		err := pool.QueryRow(ctx, "SELECT id FROM users WHERE handle = $1", handle).Scan(&existing)
		if err == nil {
			skipped++
			continue
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			logger.Error("check existing", "handle", handle, "err", err)
			return 1
		}
		_, err = pool.Exec(ctx, `INSERT INTO users (
				handle, created_at, is_sysop, is_banned,
				clock_format, date_format, temperature_unit, time_zone_id, location_source,
				password_hash, password_algo, password_updated_at,
				suppress_key_adoption_prompts, require_ssh_key
			) VALUES (
				$1, $2, FALSE, FALSE,
				0, 0, 0, 'UTC', 0,
				$3, $4, $2,
				FALSE, FALSE
			)`, handle, now, hash, algoPtr)
		if err != nil {
			logger.Error("insert", "handle", handle, "err", err)
			return 1
		}
		created++
	}
	fmt.Printf("seed: created=%d skipped=%d total=%d password=%s\n", created, skipped, *count, *password)
	return 0
}

func runClean(ctx context.Context, logger *slog.Logger) int {
	pool, err := openPool(ctx)
	if err != nil {
		logger.Error("pool", "err", err)
		return 1
	}
	defer pool.Close()
	tag, err := pool.Exec(ctx, "DELETE FROM users WHERE handle LIKE $1", handlePrefix+"%")
	if err != nil {
		logger.Error("delete", "err", err)
		return 1
	}
	fmt.Printf("clean: deleted %d loadbot users\n", tag.RowsAffected())
	return 0
}

// runRun opens -count SSH sessions in a -ramp window, holds each one for
// -duration, and reports timings. Each goroutine owns one session and yields
// only when context cancels or -duration elapses.
func runRun(ctx context.Context, args []string, logger *slog.Logger) int {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	count := fs.Int("count", 0, "concurrent sessions")
	host := fs.String("host", "127.0.0.1:2222", "ssh host:port")
	ramp := fs.Duration("ramp", 30*time.Second, "spread connect attempts over this window")
	dur := fs.Duration("duration", 60*time.Second, "hold sessions for this duration after ramp")
	password := fs.String("password", "loadtest-2026", "shared bot password")
	scenario := fs.String("scenario", "mix", "bot behavior: idle | chat | forums | mix")
	_ = fs.Parse(args)
	if *count <= 0 {
		fmt.Fprintln(os.Stderr, "run: -count required")
		return 2
	}
	scen := scenarioName(*scenario)
	switch scen {
	case scenarioIdle, scenarioChat, scenarioForums, scenarioMix:
	default:
		fmt.Fprintf(os.Stderr, "run: unknown scenario %q (want idle|chat|forums|mix)\n", *scenario)
		return 2
	}

	ctx, cancel := context.WithTimeout(ctx, *ramp+*dur+30*time.Second)
	defer cancel()

	// Spread session starts across the ramp window. Tighter ramps stress the
	// auth/handshake path; wider ramps test steady-state.
	stride := time.Duration(0)
	if *count > 1 {
		stride = *ramp / time.Duration(*count-1)
	}

	var wg sync.WaitGroup
	var connected, failed atomic.Int64
	latencies := make([]time.Duration, *count)
	errs := make(chan error, *count)
	holdUntil := time.Now().Add(*ramp).Add(*dur)

	for i := 0; i < *count; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			handle := fmt.Sprintf("%s%04d", handlePrefix, i+1)
			delay := time.Duration(i) * stride
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			// Per-bot RNG so concurrent goroutines don't contend on a
			// shared source. Seed pairs (time, i+1) so reruns diverge but
			// the per-bot stream is deterministic within a run.
			rng := rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), uint64(i+1)))
			start := time.Now()
			err := holdSession(ctx, *host, handle, *password, holdUntil, scen, rng)
			elapsed := time.Since(start)
			latencies[i] = elapsed
			if err != nil {
				failed.Add(1)
				errs <- fmt.Errorf("%s: %w", handle, err)
				return
			}
			connected.Add(1)
		}()
	}

	wg.Wait()
	close(errs)

	// Drain errs into a sample for the report; don't spam.
	var sample []error
	for err := range errs {
		if len(sample) < 5 {
			sample = append(sample, err)
		}
	}

	c := connected.Load()
	f := failed.Load()
	report(*count, int(c), int(f), latencies, sample, scen)
	if f > 0 {
		return 1
	}
	return 0
}

// holdSession opens an SSH shell, requests a PTY, runs the chosen scenario
// until holdUntil or ctx fires, then disconnects cleanly. The session never
// reads what the server renders — stdout is drained to the void; scenarios
// drive the TUI blindly by writing keystrokes into stdin.
func holdSession(ctx context.Context, host, user, password string, holdUntil time.Time, scen scenarioName, rng *rand.Rand) error {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	cfg := &gossh.ClientConfig{
		User:            user,
		Auth:            []gossh.AuthMethod{gossh.Password(password)},
		HostKeyCallback: gossh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}
	conn, err := dialer.DialContext(ctx, "tcp", host)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()
	c, chans, reqs, err := gossh.NewClientConn(conn, host, cfg)
	if err != nil {
		return fmt.Errorf("handshake: %w", err)
	}
	defer c.Close()
	client := gossh.NewClient(c, chans, reqs)
	sess, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("session: %w", err)
	}
	defer sess.Close()
	stdout, err := sess.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout: %w", err)
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin: %w", err)
	}
	if err := sess.RequestPty("xterm-256color", 24, 80, gossh.TerminalModes{gossh.ECHO: 0}); err != nil {
		return fmt.Errorf("pty: %w", err)
	}
	if err := sess.Shell(); err != nil {
		return fmt.Errorf("shell: %w", err)
	}
	// Drain stdout to the void so the server's writes don't block.
	drainDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, stdout)
		close(drainDone)
	}()
	// Let the lobby render before sending any scenario keystrokes —
	// hotkeys like 'c' are accepted before the first paint anyway, but the
	// brief warm-up avoids a thundering herd hitting the carousel pre-render.
	sleepCtx(ctx, 500*time.Millisecond)

	if err := runScenario(ctx, stdin, user, holdUntil, scen, rng); err != nil {
		return err
	}

	// Ctrl+C to land a clean disconnect (Root model intercepts it).
	_, _ = stdin.Write([]byte{0x03})
	select {
	case <-drainDone:
	case <-time.After(2 * time.Second):
	}
	return nil
}

// runScenario resolves the bot's effective scenario (mix → chat or forums)
// and dispatches. Returns nil for natural ctx / holdUntil completion;
// writeErr is the only error path — once stdin breaks the rest of the
// session is unrecoverable.
func runScenario(ctx context.Context, stdin io.Writer, handle string, holdUntil time.Time, scen scenarioName, rng *rand.Rand) error {
	effective := scen
	if effective == scenarioMix {
		if rng.IntN(100) < 70 {
			effective = scenarioChat
		} else {
			effective = scenarioForums
		}
	}
	switch effective {
	case scenarioChat:
		return runChatLoop(ctx, stdin, handle, holdUntil, rng)
	case scenarioForums:
		return runForumsLoop(ctx, stdin, holdUntil, rng)
	default:
		return runIdleLoop(ctx, holdUntil)
	}
}

// runIdleLoop is the historical behavior: hold the lobby session until the
// window closes. Useful for isolating connect / heartbeat / RSS cost from
// the chat fan-out.
func runIdleLoop(ctx context.Context, holdUntil time.Time) error {
	hold := time.Until(holdUntil)
	if hold > 0 {
		sleepCtx(ctx, hold)
	}
	return nil
}

// runChatLoop enters the Chat screen once, then posts a short message every
// 8–20s until the hold window ends. Each send fans out to every other
// chat-scenario bot on #lobby via the Redis bus.
func runChatLoop(ctx context.Context, stdin io.Writer, handle string, holdUntil time.Time, rng *rand.Rand) error {
	// Lobby hotkey 'c' enters Chat. The handler navigates immediately; no
	// confirmation needed.
	if _, err := stdin.Write([]byte{'c'}); err != nil {
		return fmt.Errorf("chat enter: %w", err)
	}
	// Give the bootstrap query + subscription a beat to land before
	// shoving a message into the input.
	if !sleepCtx(ctx, 600*time.Millisecond) {
		return nil
	}
	for time.Now().Before(holdUntil) {
		// Think time: 8–20s. Picked to match a moderately chatty user
		// while still producing a steady stream at 200 bots.
		wait := time.Duration(8+rng.IntN(13)) * time.Second
		if !sleepCtx(ctx, wait) {
			return nil
		}
		if time.Now().After(holdUntil) {
			return nil
		}
		// Carriage return submits the textinput. Keep the body short so
		// the chatlog wrap path isn't the bottleneck under test.
		body := fmt.Sprintf("ping %s %s\r", handle, time.Now().Format("15:04:05"))
		if _, err := stdin.Write([]byte(body)); err != nil {
			return fmt.Errorf("chat send: %w", err)
		}
		globalCounters.chatSends.Add(1)
	}
	return nil
}

// runForumsLoop cycles Lobby → Boards → first forum → first topic → back
// out, every 10–25s. Each cycle exercises ListForums + ListTopics + a
// posts window + the post_reads marker write.
func runForumsLoop(ctx context.Context, stdin io.Writer, holdUntil time.Time, rng *rand.Rand) error {
	for time.Now().Before(holdUntil) {
		// 'b' is the lobby's Boards hotkey.
		if _, err := stdin.Write([]byte{'b'}); err != nil {
			return fmt.Errorf("boards enter: %w", err)
		}
		if !sleepCtx(ctx, 500*time.Millisecond) {
			return nil
		}
		// Enter the first forum (cursor defaults to row 0).
		if _, err := stdin.Write([]byte("\r")); err != nil {
			return fmt.Errorf("forum open: %w", err)
		}
		if !sleepCtx(ctx, 500*time.Millisecond) {
			return nil
		}
		// Enter the first topic. If the forum is empty this is a no-op
		// on the server — still useful as load on the empty-list path.
		if _, err := stdin.Write([]byte("\r")); err != nil {
			return fmt.Errorf("topic open: %w", err)
		}
		if !sleepCtx(ctx, 800*time.Millisecond) {
			return nil
		}
		globalCounters.forumVisits.Add(1)
		// Walk back to the lobby: three Escs (thread → topics → forums →
		// lobby). Extra Escs at the lobby are no-ops.
		for i := 0; i < 3; i++ {
			if _, err := stdin.Write([]byte{0x1b}); err != nil {
				return fmt.Errorf("escape: %w", err)
			}
			if !sleepCtx(ctx, 100*time.Millisecond) {
				return nil
			}
		}
		wait := time.Duration(10+rng.IntN(16)) * time.Second
		if !sleepCtx(ctx, wait) {
			return nil
		}
	}
	return nil
}

// report prints a small summary table to stdout: counts + p50/p95/p99
// session-lifetime latency + per-scenario action totals. Session lifetime
// is dominated by the hold window since holdSession blocks for the full
// duration — useful as a "did the session last the whole duration" measure
// rather than connect latency alone.
func report(target, ok, fail int, lat []time.Duration, errs []error, scen scenarioName) {
	sorted := make([]time.Duration, 0, len(lat))
	for _, d := range lat {
		if d > 0 {
			sorted = append(sorted, d)
		}
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	p50 := pct(sorted, 50)
	p95 := pct(sorted, 95)
	p99 := pct(sorted, 99)
	fmt.Println()
	fmt.Println("=== loadtest report ===")
	fmt.Printf(" scenario          : %s\n", scen)
	fmt.Printf(" target sessions   : %d\n", target)
	fmt.Printf(" connected         : %d\n", ok)
	fmt.Printf(" failed            : %d\n", fail)
	fmt.Printf(" session p50       : %s\n", p50)
	fmt.Printf(" session p95       : %s\n", p95)
	fmt.Printf(" session p99       : %s\n", p99)
	fmt.Printf(" chat sends        : %d\n", globalCounters.chatSends.Load())
	fmt.Printf(" forum visits      : %d\n", globalCounters.forumVisits.Load())
	if len(errs) > 0 {
		fmt.Println(" sample errors:")
		for _, e := range errs {
			fmt.Println("   -", e)
		}
	}
}

func pct(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(float64(len(sorted)-1) * p / 100)
	return sorted[idx]
}

func openPool(ctx context.Context) (*pgxpool.Pool, error) {
	return pgxpool.New(ctx, config.Load().DBConnStr)
}
