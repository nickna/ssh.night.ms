using Microsoft.EntityFrameworkCore.Migrations;

#nullable disable

namespace Night.Ms.SshServer.Persistence.Migrations
{
    /// <inheritdoc />
    public partial class AddSuppressKeyAdoptionPrompts : Migration
    {
        /// <inheritdoc />
        protected override void Up(MigrationBuilder migrationBuilder)
        {
            migrationBuilder.AddColumn<bool>(
                name: "suppress_key_adoption_prompts",
                table: "users",
                type: "boolean",
                nullable: false,
                defaultValue: false);
        }

        /// <inheritdoc />
        protected override void Down(MigrationBuilder migrationBuilder)
        {
            migrationBuilder.DropColumn(
                name: "suppress_key_adoption_prompts",
                table: "users");
        }
    }
}
