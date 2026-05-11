using Microsoft.EntityFrameworkCore.Migrations;

#nullable disable

namespace Night.Ms.SshServer.Persistence.Migrations
{
    /// <inheritdoc />
    public partial class AddUserGeocodedLocation : Migration
    {
        /// <inheritdoc />
        protected override void Up(MigrationBuilder migrationBuilder)
        {
            migrationBuilder.AddColumn<string>(
                name: "location_canonical",
                table: "users",
                type: "character varying(160)",
                maxLength: 160,
                nullable: true);

            migrationBuilder.AddColumn<double>(
                name: "location_latitude",
                table: "users",
                type: "double precision",
                nullable: true);

            migrationBuilder.AddColumn<double>(
                name: "location_longitude",
                table: "users",
                type: "double precision",
                nullable: true);

            migrationBuilder.AddColumn<int>(
                name: "location_source",
                table: "users",
                type: "integer",
                nullable: false,
                defaultValue: 0);
        }

        /// <inheritdoc />
        protected override void Down(MigrationBuilder migrationBuilder)
        {
            migrationBuilder.DropColumn(
                name: "location_canonical",
                table: "users");

            migrationBuilder.DropColumn(
                name: "location_latitude",
                table: "users");

            migrationBuilder.DropColumn(
                name: "location_longitude",
                table: "users");

            migrationBuilder.DropColumn(
                name: "location_source",
                table: "users");
        }
    }
}
