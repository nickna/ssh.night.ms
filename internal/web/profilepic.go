package web

import (
	"context"
	"fmt"
	"image"
	_ "image/gif"  // register decoder for GIF source uploads (rare, but accept)
	_ "image/jpeg" // register decoder so image.Decode handles JPEG
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"golang.org/x/image/draw"

	"github.com/nickna/ssh.night.ms/internal/data/gen"
)

// profilePictureSize is the canvas the uploaded image is normalized to.
// 256x256 strikes a balance between a sharp avatar at the 128px display
// rendering and a small enough byte budget that the disk isn't a concern.
const profilePictureSize = 256

// SaveProfilePicture decodes the uploaded image, resizes it to a square at
// profilePictureSize using a high-quality scaler, and writes it as PNG to
// NIGHTMS_PFP_DIR/<user_id>.png. Also stamps users.profile_picture_updated_at
// so the avatar handler can invalidate its ETag and clients refetch.
//
// Writes go through a temp file first then rename so a half-written upload
// can never poison the existing avatar on a crash mid-write.
func (h *handlers) SaveProfilePicture(ctx context.Context, userID int64, src io.Reader) error {
	if err := os.MkdirAll(h.cfg.PFPDir, 0o755); err != nil {
		return fmt.Errorf("pfp: mkdir: %w", err)
	}

	srcImg, _, err := image.Decode(src)
	if err != nil {
		return fmt.Errorf("pfp: decode: %w", err)
	}

	// Resize keeping aspect ratio + crop to a square by fitting the shorter
	// side then center-cropping the longer one. Use the resampler from
	// x/image/draw; CatmullRom looks great on photos and not-terrible on
	// pixel art either.
	resized := resizeAndCropSquare(srcImg, profilePictureSize)

	final := filepath.Join(h.cfg.PFPDir, strconv.FormatInt(userID, 10)+".png")
	tmp := final + ".tmp"

	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("pfp: create tmp: %w", err)
	}
	if err := png.Encode(f, resized); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("pfp: encode png: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("pfp: close tmp: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("pfp: rename: %w", err)
	}

	// Bump the timestamp so the avatar handler's ETag changes and any cached
	// copies in browsers fall out.
	if _, err := h.deps.Pool.Exec(ctx,
		`UPDATE users SET profile_picture_updated_at = $2 WHERE id = $1`,
		userID, pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
	); err != nil {
		return fmt.Errorf("pfp: stamp updated_at: %w", err)
	}
	return nil
}

// ClearProfilePicture removes the user's uploaded file (if any) and zeroes
// the column so the avatar handler falls back to the identicon.
func (h *handlers) ClearProfilePicture(ctx context.Context, userID int64) error {
	final := filepath.Join(h.cfg.PFPDir, strconv.FormatInt(userID, 10)+".png")
	if err := os.Remove(final); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("pfp: remove: %w", err)
	}
	if _, err := h.deps.Pool.Exec(ctx,
		`UPDATE users SET profile_picture_updated_at = NULL WHERE id = $1`,
		userID,
	); err != nil {
		return fmt.Errorf("pfp: clear updated_at: %w", err)
	}
	return nil
}

// resizeAndCropSquare downscales `src` so its shorter dimension hits `size`
// then center-crops the longer dimension to a `size`×`size` square. We do
// the crop after the resize so the scaler sees the full pixels.
func resizeAndCropSquare(src image.Image, size int) image.Image {
	b := src.Bounds()
	srcW, srcH := b.Dx(), b.Dy()

	// Scale so the shorter side matches `size`.
	var scaledW, scaledH int
	if srcW < srcH {
		scaledW = size
		scaledH = (srcH * size) / srcW
	} else {
		scaledH = size
		scaledW = (srcW * size) / srcH
	}
	scaled := image.NewRGBA(image.Rect(0, 0, scaledW, scaledH))
	draw.CatmullRom.Scale(scaled, scaled.Bounds(), src, b, draw.Over, nil)

	// Center-crop to size×size.
	offX := (scaledW - size) / 2
	offY := (scaledH - size) / 2
	cropped := image.NewRGBA(image.Rect(0, 0, size, size))
	draw.Draw(cropped, cropped.Bounds(), scaled, image.Pt(offX, offY), draw.Src)
	return cropped
}

// readProfilePicture returns the on-disk path and modified time when the user
// has an uploaded picture; (path, time, true) on hit, ("", zero, false)
// otherwise. The decision uses users.profile_picture_updated_at — a NULL
// value means "no upload" even if a stale file lingers on disk.
func (h *handlers) readProfilePicture(ctx context.Context, user gen.User) (string, time.Time, bool) {
	if !user.ProfilePictureUpdatedAt.Valid {
		return "", time.Time{}, false
	}
	path := filepath.Join(h.cfg.PFPDir, strconv.FormatInt(user.ID, 10)+".png")
	if _, err := os.Stat(path); err != nil {
		// DB says we have one but the file is missing — log and treat as
		// no-pic so the identicon kicks in.
		h.deps.Logger.Warn("pfp: file missing for stamped user", "user_id", user.ID, "err", err)
		return "", time.Time{}, false
	}
	return path, user.ProfilePictureUpdatedAt.Time, true
}
