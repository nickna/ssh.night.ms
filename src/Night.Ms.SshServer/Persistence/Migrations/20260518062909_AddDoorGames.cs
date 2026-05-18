using System;
using System.Text.Json;
using Microsoft.EntityFrameworkCore.Migrations;
using Npgsql.EntityFrameworkCore.PostgreSQL.Metadata;

#nullable disable

namespace Night.Ms.SshServer.Persistence.Migrations
{
    /// <inheritdoc />
    public partial class AddDoorGames : Migration
    {
        /// <inheritdoc />
        protected override void Up(MigrationBuilder migrationBuilder)
        {
            migrationBuilder.CreateTable(
                name: "game_rounds",
                columns: table => new
                {
                    id = table.Column<long>(type: "bigint", nullable: false)
                        .Annotation("Npgsql:ValueGenerationStrategy", NpgsqlValueGenerationStrategy.IdentityByDefaultColumn),
                    user_id = table.Column<long>(type: "bigint", nullable: false),
                    game_key = table.Column<string>(type: "character varying(32)", maxLength: 32, nullable: false),
                    bet = table.Column<int>(type: "integer", nullable: false),
                    payout = table.Column<int>(type: "integer", nullable: false),
                    net = table.Column<int>(type: "integer", nullable: false),
                    details = table.Column<JsonDocument>(type: "jsonb", nullable: true),
                    played_at = table.Column<DateTimeOffset>(type: "timestamp with time zone", nullable: false)
                },
                constraints: table =>
                {
                    table.PrimaryKey("pk_game_rounds", x => x.id);
                    table.ForeignKey(
                        name: "fk_game_rounds_users_user_id",
                        column: x => x.user_id,
                        principalTable: "users",
                        principalColumn: "id",
                        onDelete: ReferentialAction.Cascade);
                });

            migrationBuilder.CreateTable(
                name: "user_wallets",
                columns: table => new
                {
                    user_id = table.Column<long>(type: "bigint", nullable: false),
                    daily_credits = table.Column<int>(type: "integer", nullable: false),
                    daily_credits_refreshed_on = table.Column<DateOnly>(type: "date", nullable: true),
                    winnings_balance = table.Column<long>(type: "bigint", nullable: false),
                    updated_at = table.Column<DateTimeOffset>(type: "timestamp with time zone", nullable: false)
                },
                constraints: table =>
                {
                    table.PrimaryKey("pk_user_wallets", x => x.user_id);
                    table.ForeignKey(
                        name: "fk_user_wallets_users_user_id",
                        column: x => x.user_id,
                        principalTable: "users",
                        principalColumn: "id",
                        onDelete: ReferentialAction.Cascade);
                });

            migrationBuilder.CreateIndex(
                name: "ix_game_rounds_game_key_net",
                table: "game_rounds",
                columns: new[] { "game_key", "net" },
                descending: new[] { false, true });

            migrationBuilder.CreateIndex(
                name: "ix_game_rounds_game_key_played_at",
                table: "game_rounds",
                columns: new[] { "game_key", "played_at" },
                descending: new[] { false, true });

            migrationBuilder.CreateIndex(
                name: "ix_game_rounds_user_id_played_at",
                table: "game_rounds",
                columns: new[] { "user_id", "played_at" },
                descending: new[] { false, true });
        }

        /// <inheritdoc />
        protected override void Down(MigrationBuilder migrationBuilder)
        {
            migrationBuilder.DropTable(
                name: "game_rounds");

            migrationBuilder.DropTable(
                name: "user_wallets");
        }
    }
}
