using System;
using System.Text.Json;
using Microsoft.EntityFrameworkCore.Migrations;
using Npgsql.EntityFrameworkCore.PostgreSQL.Metadata;

#nullable disable

namespace Night.Ms.SshServer.Persistence.Migrations
{
    /// <inheritdoc />
    public partial class AddMultiplayerHandsAndLinkage : Migration
    {
        /// <inheritdoc />
        protected override void Up(MigrationBuilder migrationBuilder)
        {
            migrationBuilder.AddColumn<long>(
                name: "hand_id",
                table: "game_rounds",
                type: "bigint",
                nullable: true);

            migrationBuilder.CreateTable(
                name: "multiplayer_hands",
                columns: table => new
                {
                    id = table.Column<long>(type: "bigint", nullable: false)
                        .Annotation("Npgsql:ValueGenerationStrategy", NpgsqlValueGenerationStrategy.IdentityByDefaultColumn),
                    game_key = table.Column<string>(type: "character varying(32)", maxLength: 32, nullable: false),
                    table_id = table.Column<long>(type: "bigint", nullable: false),
                    hand_no = table.Column<long>(type: "bigint", nullable: false),
                    details = table.Column<JsonDocument>(type: "jsonb", nullable: false),
                    settled_at = table.Column<DateTimeOffset>(type: "timestamp with time zone", nullable: false)
                },
                constraints: table =>
                {
                    table.PrimaryKey("pk_multiplayer_hands", x => x.id);
                });

            migrationBuilder.CreateIndex(
                name: "ix_game_rounds_hand_id",
                table: "game_rounds",
                column: "hand_id");

            migrationBuilder.CreateIndex(
                name: "ix_multiplayer_hands_game_key_table_id_hand_no",
                table: "multiplayer_hands",
                columns: new[] { "game_key", "table_id", "hand_no" },
                unique: true);

            migrationBuilder.AddForeignKey(
                name: "fk_game_rounds_multiplayer_hands_hand_id",
                table: "game_rounds",
                column: "hand_id",
                principalTable: "multiplayer_hands",
                principalColumn: "id",
                onDelete: ReferentialAction.SetNull);
        }

        /// <inheritdoc />
        protected override void Down(MigrationBuilder migrationBuilder)
        {
            migrationBuilder.DropForeignKey(
                name: "fk_game_rounds_multiplayer_hands_hand_id",
                table: "game_rounds");

            migrationBuilder.DropTable(
                name: "multiplayer_hands");

            migrationBuilder.DropIndex(
                name: "ix_game_rounds_hand_id",
                table: "game_rounds");

            migrationBuilder.DropColumn(
                name: "hand_id",
                table: "game_rounds");
        }
    }
}
