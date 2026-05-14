using System;
using Microsoft.EntityFrameworkCore.Migrations;
using Npgsql.EntityFrameworkCore.PostgreSQL.Metadata;

#nullable disable

namespace Night.Ms.SshServer.Persistence.Migrations
{
    /// <inheritdoc />
    public partial class AddIdentityCredentials : Migration
    {
        /// <inheritdoc />
        protected override void Up(MigrationBuilder migrationBuilder)
        {
            // Order matters: create the new table first, backfill rows from ssh_keys, THEN
            // drop ssh_keys. The default EF-generated migration dropped first, which would
            // wipe every existing SSH user.
            migrationBuilder.AddColumn<string>(
                name: "email",
                table: "users",
                type: "citext",
                maxLength: 254,
                nullable: true);

            migrationBuilder.CreateTable(
                name: "identity_credentials",
                columns: table => new
                {
                    id = table.Column<long>(type: "bigint", nullable: false)
                        .Annotation("Npgsql:ValueGenerationStrategy", NpgsqlValueGenerationStrategy.IdentityByDefaultColumn),
                    user_id = table.Column<long>(type: "bigint", nullable: false),
                    provider = table.Column<string>(type: "character varying(32)", maxLength: 32, nullable: false),
                    subject = table.Column<string>(type: "character varying(255)", maxLength: 255, nullable: false),
                    metadata = table.Column<string>(type: "jsonb", nullable: true),
                    label = table.Column<string>(type: "character varying(80)", maxLength: 80, nullable: true),
                    created_at = table.Column<DateTimeOffset>(type: "timestamp with time zone", nullable: false),
                    last_used_at = table.Column<DateTimeOffset>(type: "timestamp with time zone", nullable: true)
                },
                constraints: table =>
                {
                    table.PrimaryKey("pk_identity_credentials", x => x.id);
                    table.ForeignKey(
                        name: "fk_identity_credentials_users_user_id",
                        column: x => x.user_id,
                        principalTable: "users",
                        principalColumn: "id",
                        onDelete: ReferentialAction.Cascade);
                });

            migrationBuilder.CreateIndex(
                name: "ix_users_email",
                table: "users",
                column: "email",
                unique: true);

            migrationBuilder.CreateIndex(
                name: "ix_identity_credentials_provider_subject",
                table: "identity_credentials",
                columns: new[] { "provider", "subject" },
                unique: true);

            migrationBuilder.CreateIndex(
                name: "ix_identity_credentials_user_id",
                table: "identity_credentials",
                column: "user_id");

            // Backfill every existing ssh_keys row as an IdentityCredential with Provider='Ssh'
            // (string-encoded enum). The original algorithm + public key blob are stashed in
            // metadata jsonb so a future "show my keys" UI can render them without a schema
            // change. Base64 keeps the blob safe inside a text-y jsonb value.
            migrationBuilder.Sql(@"
                INSERT INTO identity_credentials (user_id, provider, subject, metadata, label, created_at)
                SELECT
                    user_id,
                    'Ssh',
                    fingerprint,
                    jsonb_build_object(
                        'algorithm', key_type,
                        'blob_b64', encode(public_key_blob, 'base64')
                    ),
                    label,
                    added_at
                FROM ssh_keys;
            ");

            migrationBuilder.DropTable(
                name: "ssh_keys");
        }

        /// <inheritdoc />
        protected override void Down(MigrationBuilder migrationBuilder)
        {
            migrationBuilder.DropTable(
                name: "identity_credentials");

            migrationBuilder.DropIndex(
                name: "ix_users_email",
                table: "users");

            migrationBuilder.DropColumn(
                name: "email",
                table: "users");

            migrationBuilder.CreateTable(
                name: "ssh_keys",
                columns: table => new
                {
                    id = table.Column<long>(type: "bigint", nullable: false)
                        .Annotation("Npgsql:ValueGenerationStrategy", NpgsqlValueGenerationStrategy.IdentityByDefaultColumn),
                    user_id = table.Column<long>(type: "bigint", nullable: false),
                    added_at = table.Column<DateTimeOffset>(type: "timestamp with time zone", nullable: false),
                    fingerprint = table.Column<string>(type: "character varying(80)", maxLength: 80, nullable: false),
                    key_type = table.Column<string>(type: "character varying(64)", maxLength: 64, nullable: false),
                    label = table.Column<string>(type: "text", nullable: true),
                    public_key_blob = table.Column<byte[]>(type: "bytea", nullable: false)
                },
                constraints: table =>
                {
                    table.PrimaryKey("pk_ssh_keys", x => x.id);
                    table.ForeignKey(
                        name: "fk_ssh_keys_users_user_id",
                        column: x => x.user_id,
                        principalTable: "users",
                        principalColumn: "id",
                        onDelete: ReferentialAction.Cascade);
                });

            migrationBuilder.CreateIndex(
                name: "ix_ssh_keys_fingerprint",
                table: "ssh_keys",
                column: "fingerprint",
                unique: true);

            migrationBuilder.CreateIndex(
                name: "ix_ssh_keys_user_id",
                table: "ssh_keys",
                column: "user_id");
        }
    }
}
