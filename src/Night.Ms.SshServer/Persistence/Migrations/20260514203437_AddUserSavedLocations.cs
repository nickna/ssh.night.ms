using System;
using Microsoft.EntityFrameworkCore.Migrations;
using Npgsql.EntityFrameworkCore.PostgreSQL.Metadata;

#nullable disable

namespace Night.Ms.SshServer.Persistence.Migrations
{
    /// <inheritdoc />
    public partial class AddUserSavedLocations : Migration
    {
        /// <inheritdoc />
        protected override void Up(MigrationBuilder migrationBuilder)
        {
            migrationBuilder.CreateTable(
                name: "user_saved_locations",
                columns: table => new
                {
                    id = table.Column<long>(type: "bigint", nullable: false)
                        .Annotation("Npgsql:ValueGenerationStrategy", NpgsqlValueGenerationStrategy.IdentityByDefaultColumn),
                    user_id = table.Column<long>(type: "bigint", nullable: false),
                    label = table.Column<string>(type: "character varying(64)", maxLength: 64, nullable: false),
                    latitude = table.Column<double>(type: "double precision", nullable: false),
                    longitude = table.Column<double>(type: "double precision", nullable: false),
                    canonical = table.Column<string>(type: "character varying(160)", maxLength: 160, nullable: true),
                    sort_order = table.Column<int>(type: "integer", nullable: false),
                    created_at = table.Column<DateTimeOffset>(type: "timestamp with time zone", nullable: false)
                },
                constraints: table =>
                {
                    table.PrimaryKey("pk_user_saved_locations", x => x.id);
                    table.ForeignKey(
                        name: "fk_user_saved_locations_users_user_id",
                        column: x => x.user_id,
                        principalTable: "users",
                        principalColumn: "id",
                        onDelete: ReferentialAction.Cascade);
                });

            migrationBuilder.CreateIndex(
                name: "ix_user_saved_locations_user_id_label",
                table: "user_saved_locations",
                columns: new[] { "user_id", "label" },
                unique: true);

            migrationBuilder.CreateIndex(
                name: "ix_user_saved_locations_user_id_sort_order",
                table: "user_saved_locations",
                columns: new[] { "user_id", "sort_order" });
        }

        /// <inheritdoc />
        protected override void Down(MigrationBuilder migrationBuilder)
        {
            migrationBuilder.DropTable(
                name: "user_saved_locations");
        }
    }
}
