namespace Night.Ms.SshServer.Domain;

public sealed class Forum
{
    public long Id { get; set; }
    public required string Name { get; set; }
    public string? Description { get; set; }
    public int SortOrder { get; set; }

    public List<Topic> Topics { get; set; } = [];
}

public sealed class Topic
{
    public long Id { get; set; }
    public long ForumId { get; set; }
    public required string Title { get; set; }
    public long CreatedById { get; set; }
    public DateTimeOffset CreatedAt { get; set; }
    public DateTimeOffset LastPostAt { get; set; }

    public Forum? Forum { get; set; }
    public User? CreatedBy { get; set; }
    public List<Post> Posts { get; set; } = [];
}

public sealed class Post
{
    public long Id { get; set; }
    public long TopicId { get; set; }
    public long? ParentPostId { get; set; }
    public required string Body { get; set; }
    public long CreatedById { get; set; }
    public DateTimeOffset CreatedAt { get; set; }
    public DateTimeOffset? EditedAt { get; set; }

    public Topic? Topic { get; set; }
    public Post? ParentPost { get; set; }
    public User? CreatedBy { get; set; }
}

public sealed class PostRead
{
    public long UserId { get; set; }
    public long TopicId { get; set; }
    public DateTimeOffset LastReadAt { get; set; }

    public User? User { get; set; }
    public Topic? Topic { get; set; }
}
