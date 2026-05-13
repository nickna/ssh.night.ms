using Microsoft.EntityFrameworkCore.Migrations;

#nullable disable

namespace Night.Ms.SshServer.Persistence.Migrations
{
    /// <inheritdoc />
    public partial class AddChatThreadsAndFts : Migration
    {
        /// <inheritdoc />
        protected override void Up(MigrationBuilder migrationBuilder)
        {
            migrationBuilder.AddColumn<long>(
                name: "parent_message_id",
                table: "chat_messages",
                type: "bigint",
                nullable: true);

            migrationBuilder.CreateIndex(
                name: "ix_chat_messages_parent_message_id",
                table: "chat_messages",
                column: "parent_message_id");

            migrationBuilder.AddForeignKey(
                name: "fk_chat_messages_chat_messages_parent_message_id",
                table: "chat_messages",
                column: "parent_message_id",
                principalTable: "chat_messages",
                principalColumn: "id",
                onDelete: ReferentialAction.Restrict);

            // Generated tsvector column + GIN index for /search. Hand-rolled SQL because EF
            // doesn't model Postgres generated columns. STORED makes the index lookups cheap
            // (no per-query to_tsvector). 'english' enables stemming so "migration" matches
            // "migrations" and "running" matches "ran"; the Snowball stemmer doesn't strip
            // prefixes so "build" still doesn't collide with "rebuild" (the prior ILIKE
            // implementation did over-match on substrings).
            migrationBuilder.Sql(
                "ALTER TABLE chat_messages " +
                "ADD COLUMN body_search tsvector " +
                "GENERATED ALWAYS AS (to_tsvector('english', body)) STORED;");
            migrationBuilder.Sql(
                "CREATE INDEX ix_chat_messages_body_search " +
                "ON chat_messages USING GIN (body_search);");
        }

        /// <inheritdoc />
        protected override void Down(MigrationBuilder migrationBuilder)
        {
            migrationBuilder.Sql("DROP INDEX IF EXISTS ix_chat_messages_body_search;");
            migrationBuilder.Sql("ALTER TABLE chat_messages DROP COLUMN IF EXISTS body_search;");

            migrationBuilder.DropForeignKey(
                name: "fk_chat_messages_chat_messages_parent_message_id",
                table: "chat_messages");

            migrationBuilder.DropIndex(
                name: "ix_chat_messages_parent_message_id",
                table: "chat_messages");

            migrationBuilder.DropColumn(
                name: "parent_message_id",
                table: "chat_messages");
        }
    }
}
