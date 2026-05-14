# One-shot helper: writes the lobby-icons/weather.ans file with real ESC bytes.
# Run after generate-weather-art.ps1 (or independently — order doesn't matter).

$ESC = [char]27
$BRIGHT_YELLOW = "$ESC[93m"
$WHITE = "$ESC[97m"
$RESET = "$ESC[0m"

$lines = @(
    "$BRIGHT_YELLOW  .--.  $RESET"
    "$BRIGHT_YELLOW -(    )-$RESET"
    "$WHITE(______)$RESET"
)

$out = Join-Path $PSScriptRoot "..\src\Night.Ms.SshServer\art\lobby-icons\weather.ans"
$body = [string]::Join("`n", $lines) + "`n"
[System.IO.File]::WriteAllText($out, $body, [System.Text.UTF8Encoding]::new($false))
Write-Host "wrote $out"
