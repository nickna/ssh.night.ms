using Microsoft.EntityFrameworkCore;
using Night.Ms.SshServer.Domain;

namespace Night.Ms.SshServer.Persistence;

public sealed class AppDbContext(DbContextOptions<AppDbContext> options) : DbContext(options)
{
    public DbSet<User> Users => Set<User>();
    public DbSet<SshKey> SshKeys => Set<SshKey>();
    public DbSet<Channel> Channels => Set<Channel>();
    public DbSet<ChannelMember> ChannelMembers => Set<ChannelMember>();
    public DbSet<ChatMessage> ChatMessages => Set<ChatMessage>();
    public DbSet<Forum> Forums => Set<Forum>();
    public DbSet<Topic> Topics => Set<Topic>();
    public DbSet<Post> Posts => Set<Post>();
    public DbSet<PostRead> PostReads => Set<PostRead>();
    public DbSet<AuditLog> AuditLogs => Set<AuditLog>();

    protected override void OnModelCreating(ModelBuilder modelBuilder)
    {
        // Case-insensitive handle/channel name comparisons land in the database via citext.
        modelBuilder.HasPostgresExtension("citext");

        modelBuilder.Entity<User>(b =>
        {
            b.ToTable("users");
            b.Property(u => u.Handle).HasColumnType("citext").HasMaxLength(32);
            b.Property(u => u.Bio).HasMaxLength(500);
            b.Property(u => u.Location).HasMaxLength(64);
            b.Property(u => u.RealName).HasMaxLength(64);
            b.Property(u => u.TimeZoneId).HasMaxLength(64).HasDefaultValue("UTC");
            b.Property(u => u.TemperatureUnit).HasDefaultValue(TemperatureUnit.Celsius);
            b.Property(u => u.ClockFormat).HasDefaultValue(ClockFormat.Hours24);
            b.Property(u => u.DateFormat).HasDefaultValue(DateFormat.Iso);
            b.HasIndex(u => u.Handle).IsUnique();
        });

        modelBuilder.Entity<SshKey>(b =>
        {
            b.ToTable("ssh_keys");
            b.Property(k => k.KeyType).HasMaxLength(64);
            b.Property(k => k.Fingerprint).HasMaxLength(80);
            b.HasIndex(k => k.Fingerprint).IsUnique();
            b.HasOne(k => k.User).WithMany(u => u.Keys).HasForeignKey(k => k.UserId).OnDelete(DeleteBehavior.Cascade);
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
    }
}
