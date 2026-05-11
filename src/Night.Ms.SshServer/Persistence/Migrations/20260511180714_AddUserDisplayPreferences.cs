using Microsoft.EntityFrameworkCore.Migrations;

#nullable disable

namespace Night.Ms.SshServer.Persistence.Migrations
{
    /// <inheritdoc />
    public partial class AddUserDisplayPreferences : Migration
    {
        /// <inheritdoc />
        protected override void Up(MigrationBuilder migrationBuilder)
        {
            migrationBuilder.AddColumn<int>(
                name: "clock_format",
                table: "users",
                type: "integer",
                nullable: false,
                defaultValue: 0);

            migrationBuilder.AddColumn<int>(
                name: "date_format",
                table: "users",
                type: "integer",
                nullable: false,
                defaultValue: 0);

            migrationBuilder.AddColumn<int>(
                name: "temperature_unit",
                table: "users",
                type: "integer",
                nullable: false,
                defaultValue: 0);

            migrationBuilder.AddColumn<string>(
                name: "time_zone_id",
                table: "users",
                type: "character varying(64)",
                maxLength: 64,
                nullable: false,
                defaultValue: "UTC");
        }

        /// <inheritdoc />
        protected override void Down(MigrationBuilder migrationBuilder)
        {
            migrationBuilder.DropColumn(
                name: "clock_format",
                table: "users");

            migrationBuilder.DropColumn(
                name: "date_format",
                table: "users");

            migrationBuilder.DropColumn(
                name: "temperature_unit",
                table: "users");

            migrationBuilder.DropColumn(
                name: "time_zone_id",
                table: "users");
        }
    }
}
