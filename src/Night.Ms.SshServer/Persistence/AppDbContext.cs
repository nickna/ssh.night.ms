using Microsoft.EntityFrameworkCore;
using Night.Ms.SshServer.Domain;

namespace Night.Ms.SshServer.Persistence;

public sealed class AppDbContext(DbContextOptions<AppDbContext> options) : DbContext(options)
{
    public DbSet<User> Users => Set<User>();
    public DbSet<IdentityCredential> IdentityCredentials => Set<IdentityCredential>();
    public DbSet<Channel> Channels => Set<Channel>();
    public DbSet<ChannelMember> ChannelMembers => Set<ChannelMember>();
    public DbSet<ChatMessage> ChatMessages => Set<ChatMessage>();
    public DbSet<MessageReaction> MessageReactions => Set<MessageReaction>();
    public DbSet<ChannelRead> ChannelReads => Set<ChannelRead>();
    public DbSet<Forum> Forums => Set<Forum>();
    public DbSet<Topic> Topics => Set<Topic>();
    public DbSet<Post> Posts => Set<Post>();
    public DbSet<PostRead> PostReads => Set<PostRead>();
    public DbSet<AuditLog> AuditLogs => Set<AuditLog>();
    public DbSet<UserSavedLocation> UserSavedLocations => Set<UserSavedLocation>();
    public DbSet<UserWatchlistItem> UserWatchlistItems => Set<UserWatchlistItem>();

    protected override void OnModelCreating(ModelBuilder modelBuilder)
    {
        // Case-insensitive handle/channel name comparisons land in the database via citext.
        modelBuilder.HasPostgresExtension("citext");

        modelBuilder.Entity<User>(b =>
        {
            b.ToTable("users");
            b.Property(u => u.Handle).HasColumnType("citext").HasMaxLength(32);
            b.Property(u => u.Email).HasColumnType("citext").HasMaxLength(254);
            b.Property(u => u.Bio).HasMaxLength(500);
            b.Property(u => u.Location).HasMaxLength(64);
            b.Property(u => u.LocationCanonical).HasMaxLength(160);
            b.Property(u => u.LocationSource).HasDefaultValue(LocationSource.None);
            b.Property(u => u.RealName).HasMaxLength(64);
            b.Property(u => u.PasswordAlgo).HasMaxLength(64);
            b.Property(u => u.TimeZoneId).HasMaxLength(64).HasDefaultValue("UTC");
            b.Property(u => u.TemperatureUnit).HasDefaultValue(TemperatureUnit.Celsius);
            b.Property(u => u.ClockFormat).HasDefaultValue(ClockFormat.Hours24);
            b.Property(u => u.DateFormat).HasDefaultValue(DateFormat.Iso);
            b.Property(u => u.SuppressKeyAdoptionPrompts).HasDefaultValue(false);
            b.HasIndex(u => u.Handle).IsUnique();
            b.HasIndex(u => u.Email).IsUnique();
        });

        modelBuilder.Entity<IdentityCredential>(b =>
        {
            b.ToTable("identity_credentials");
            // Enum stored as string so the table reads cleanly in psql and is robust to
            // future enum reordering. Subject stays case-sensitive — SSH fingerprints embed
            // base64 (case is meaningful), and OIDC subject claims are opaque IDs the
            // provider returns verbatim. citext would create false-positive collisions on
            // keys whose base64 differs only in case.
            b.Property(c => c.Provider).HasConversion<string>().HasMaxLength(32);
            b.Property(c => c.Subject).HasMaxLength(255);
            b.Property(c => c.Metadata).HasColumnType("jsonb");
            b.Property(c => c.Label).HasMaxLength(80);
            b.HasIndex(c => new { c.Provider, c.Subject }).IsUnique();
            b.HasIndex(c => c.UserId);
            b.HasOne(c => c.User).WithMany(u => u.Credentials).HasForeignKey(c => c.UserId).OnDelete(DeleteBehavior.Cascade);
        });

        modelBuilder.Entity<Channel>(b =>
        {
            b.ToTable("channels");
            b.Property(c => c.Name).HasColumnType("citext").HasMaxLength(48);
            b.HasIndex(c => c.Name).IsUnique();
            b.HasOne(c => c.CreatedBy).WithMany().HasForeignKey(c => c.CreatedById).OnDelete(DeleteBehavior.SetNull);
        });

        modelBuilder.Entity<ChannelMember>(b =>
        {
            b.ToTable("channel_members");
            b.HasKey(m => new { m.ChannelId, m.UserId });
            b.Property(m => m.Role).HasMaxLength(32);
            b.HasOne(m => m.Channel).WithMany(c => c.Members).HasForeignKey(m => m.ChannelId).OnDelete(DeleteBehavior.Cascade);
            b.HasOne(m => m.User).WithMany().HasForeignKey(m => m.UserId).OnDelete(DeleteBehavior.Cascade);
        });

        modelBuilder.Entity<ChatMessage>(b =>
        {
            b.ToTable("chat_messages");
            b.HasIndex(m => new { m.ChannelId, m.CreatedAt }).IsDescending(false, true);
            b.HasOne(m => m.Channel).WithMany(c => c.Messages).HasForeignKey(m => m.ChannelId).OnDelete(DeleteBehavior.Cascade);
            b.HasOne(m => m.User).WithMany().HasForeignKey(m => m.UserId).OnDelete(DeleteBehavior.Restrict);
            // Reply chains: parent FK is intentionally Restrict (not Cascade) so deleting a
            // parent doesn't wipe its thread. The renderer paints "↳ @alice (deleted): ..."
            // when the resolved parent is tombstoned.
            b.HasOne(m => m.ParentMessage).WithMany().HasForeignKey(m => m.ParentMessageId).OnDelete(DeleteBehavior.Restrict);
            b.HasIndex(m => m.ParentMessageId);
        });

        modelBuilder.Entity<ChannelRead>(b =>
        {
            b.ToTable("channel_reads");
            b.HasKey(r => new { r.UserId, r.ChannelId });
            b.HasOne(r => r.User).WithMany().HasForeignKey(r => r.UserId).OnDelete(DeleteBehavior.Cascade);
            b.HasOne(r => r.Channel).WithMany().HasForeignKey(r => r.ChannelId).OnDelete(DeleteBehavior.Cascade);
            // Sidebar query "channels this user has touched, newest activity first" rides
            // this index. Without it we'd scan the full table per session refresh.
            b.HasIndex(r => new { r.UserId, r.UpdatedAt }).IsDescending(false, true);
        });

        modelBuilder.Entity<MessageReaction>(b =>
        {
            b.ToTable("message_reactions");
            // Composite PK enforces "one user, one emoji per message" — re-adding the same
            // reaction is a no-op insert that we let the DB reject.
            b.HasKey(r => new { r.MessageId, r.UserId, r.Emoji });
            // Emoji is a unicode glyph (often 1–4 codepoints + ZWJ). 32 bytes covers every
            // sequence in our curated EmojiTable with room for skin-tone modifiers later.
            b.Property(r => r.Emoji).HasMaxLength(32);
            b.HasIndex(r => r.MessageId); // for "show me all reactions for these messages"
            b.HasOne(r => r.Message).WithMany(m => m.Reactions).HasForeignKey(r => r.MessageId).OnDelete(DeleteBehavior.Cascade);
            b.HasOne(r => r.User).WithMany().HasForeignKey(r => r.UserId).OnDelete(DeleteBehavior.Cascade);
        });

        modelBuilder.Entity<Forum>(b =>
        {
            b.ToTable("forums");
            b.Property(f => f.Name).HasMaxLength(64);
            b.HasIndex(f => f.Name).IsUnique();
        });

        modelBuilder.Entity<Topic>(b =>
        {
            b.ToTable("topics");
            b.Property(t => t.Title).HasMaxLength(120);
            b.HasIndex(t => new { t.ForumId, t.LastPostAt }).IsDescending(false, true);
            b.HasOne(t => t.Forum).WithMany(f => f.Topics).HasForeignKey(t => t.ForumId).OnDelete(DeleteBehavior.Cascade);
            b.HasOne(t => t.CreatedBy).WithMany().HasForeignKey(t => t.CreatedById).OnDelete(DeleteBehavior.Restrict);
        });

        modelBuilder.Entity<Post>(b =>
        {
            b.ToTable("posts");
            b.HasOne(p => p.Topic).WithMany(t => t.Posts).HasForeignKey(p => p.TopicId).OnDelete(DeleteBehavior.Cascade);
            b.HasOne(p => p.ParentPost).WithMany().HasForeignKey(p => p.ParentPostId).OnDelete(DeleteBehavior.Restrict);
            b.HasOne(p => p.CreatedBy).WithMany().HasForeignKey(p => p.CreatedById).OnDelete(DeleteBehavior.Restrict);
        });

        modelBuilder.Entity<PostRead>(b =>
        {
            b.ToTable("post_reads");
            b.HasKey(r => new { r.UserId, r.TopicId });
            b.HasOne(r => r.User).WithMany().HasForeignKey(r => r.UserId).OnDelete(DeleteBehavior.Cascade);
            b.HasOne(r => r.Topic).WithMany().HasForeignKey(r => r.TopicId).OnDelete(DeleteBehavior.Cascade);
        });

        modelBuilder.Entity<AuditLog>(b =>
        {
            b.ToTable("audit_log");
            b.Property(a => a.Action).HasMaxLength(64);
            b.Property(a => a.TargetType).HasMaxLength(64);
            b.Property(a => a.Details).HasColumnType("jsonb");
            b.HasOne(a => a.Actor).WithMany().HasForeignKey(a => a.ActorId).OnDelete(DeleteBehavior.SetNull);
        });

        modelBuilder.Entity<UserSavedLocation>(b =>
        {
            b.ToTable("user_saved_locations");
            b.Property(s => s.Label).HasMaxLength(64);
            b.Property(s => s.Canonical).HasMaxLength(160);
            // Per-user uniqueness on label so "Tokyo" can't appear twice under one user.
            // SortOrder drives the F1..F9 keypad mapping on WeatherScreen; an index on the
            // FK + sort helps the per-user "list my favorites in order" query that runs
            // once per screen open.
            b.HasIndex(s => new { s.UserId, s.Label }).IsUnique();
            b.HasIndex(s => new { s.UserId, s.SortOrder });
            b.HasOne(s => s.User).WithMany(u => u.SavedLocations).HasForeignKey(s => s.UserId).OnDelete(DeleteBehavior.Cascade);
        });

        modelBuilder.Entity<UserWatchlistItem>(b =>
        {
            b.ToTable("user_watchlist_items");
            b.Property(w => w.Symbol).HasMaxLength(32);
            b.Property(w => w.Canonical).HasMaxLength(64);
            // Per-user uniqueness on Canonical: "BTC" and "c:bitcoin" both resolve to
            // "bitcoin" and should collide. The raw Symbol is allowed to vary across users.
            // The sort index serves the "list this user's rows in order" query that runs
            // every time FinanceScreen opens.
            b.HasIndex(w => new { w.UserId, w.Canonical }).IsUnique();
            b.HasIndex(w => new { w.UserId, w.SortOrder });
            b.HasOne(w => w.User).WithMany(u => u.Watchlist).HasForeignKey(w => w.UserId).OnDelete(DeleteBehavior.Cascade);
        });
    }
}
