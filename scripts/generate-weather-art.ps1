# Generates the seed `.ans` files for the WeatherScreen animation gallery.
#
# We do this in a script (rather than hand-editing .ans files) because the SGR escapes
# embed an ESC byte (0x1b), which most editors render as garbage and most tools strip on
# write. Run once after a checkout to produce / refresh the seed frames; sysop can layer
# custom art on top by adding files alongside the generated ones.
#
# Usage:
#   pwsh ./scripts/generate-weather-art.ps1

param(
    [string]$OutputRoot = (Join-Path $PSScriptRoot "..\src\Night.Ms.SshServer\art\weather")
)

$ErrorActionPreference = 'Stop'
$ESC = [char]27

function Sgr([int[]]$codes) {
    return "$ESC[$([string]::Join(';', $codes))m"
}

$RESET   = Sgr 0
$BRIGHT_YELLOW = Sgr 93
$YELLOW  = Sgr 33
$WHITE   = Sgr 97
$GRAY    = Sgr 37
$DARKGRAY= Sgr 90
$BLUE    = Sgr 94
$DARKBLUE= Sgr 34
$CYAN    = Sgr 96
$BRIGHT_WHITE_BOLD = Sgr @(1, 97)
$BRIGHT_RED = Sgr 91
$BRIGHT_CYAN = Sgr 96
$BOLD_YELLOW = Sgr @(1, 93)

function Pad([string]$line, [int]$width = 40) {
    # Pad to a visible width based on rune count; SGR escapes don't count.
    $stripped = [regex]::Replace($line, "$ESC\[[0-9;]*m", '')
    $visibleLen = $stripped.Length
    if ($visibleLen -ge $width) { return $line }
    return $line + (' ' * ($width - $visibleLen))
}

function WriteFrame([string]$slug, [int]$index, [string[]]$lines) {
    $dir = Join-Path $OutputRoot $slug
    if (-not (Test-Path $dir)) { New-Item -ItemType Directory -Path $dir -Force | Out-Null }
    $padded = $lines | ForEach-Object { Pad $_ 40 }
    $body = [string]::Join("`n", $padded) + "`n"
    $file = Join-Path $dir ("frame-{0:00}.ans" -f $index)
    [System.IO.File]::WriteAllText($file, $body, [System.Text.UTF8Encoding]::new($false))
    Write-Host "wrote $file"
}

# ---------- clear-day: sun with rotating rays (4 visibly distinct frames) ----------
# Each frame moves the ray pattern through a clear sequence so the eye reads it as motion:
# (1) NSEW long rays, (2) diagonal rays, (3) burst with sparkles, (4) NSEW long rays again
# offset for a slow pulse. Outer ray tips use bright red for warm/orange glow, inner uses
# bright yellow, sparkles use white. Sun face stays bold yellow on every frame.

WriteFrame "clear-day" 1 @(
    "        $BRIGHT_RED \\$BRIGHT_YELLOW    $BRIGHT_RED|$BRIGHT_YELLOW    $BRIGHT_RED/$RESET                  "
    "        $BRIGHT_YELLOW  \\   |   /  $RESET                "
    "        $BOLD_YELLOW    .---.    $RESET                  "
    "  $BRIGHT_RED---$BRIGHT_YELLOW---$BOLD_YELLOW(  *  )$BRIGHT_YELLOW---$BRIGHT_RED---$RESET            "
    "        $BOLD_YELLOW    '---'    $RESET                  "
    "        $BRIGHT_YELLOW  /   |   \\  $RESET                "
)

WriteFrame "clear-day" 2 @(
    "  $BRIGHT_RED\\$BRIGHT_YELLOW                            $BRIGHT_RED/$RESET  "
    "    $BRIGHT_YELLOW\\$WHITE *  $BRIGHT_YELLOW          $WHITE  * $BRIGHT_YELLOW/    $RESET           "
    "        $BOLD_YELLOW    .---.    $RESET                  "
    "        $BOLD_YELLOW   (     )   $RESET                  "
    "        $BOLD_YELLOW    '---'    $RESET                  "
    "    $BRIGHT_YELLOW/$WHITE *  $BRIGHT_YELLOW          $WHITE  * $BRIGHT_YELLOW\\$BRIGHT_RED            $RESET"
)

WriteFrame "clear-day" 3 @(
    "  $WHITE*$BRIGHT_RED   \\$BRIGHT_YELLOW  |  /$BRIGHT_RED   $WHITE*$RESET                       "
    "      $BRIGHT_YELLOW  \\ | /  $RESET                      "
    "  $WHITE*$BRIGHT_RED -- $BOLD_YELLOW.---. $BRIGHT_RED-- $WHITE*$RESET                       "
    "        $BOLD_YELLOW(  ☼  )$RESET                          "
    "  $WHITE*$BRIGHT_RED -- $BOLD_YELLOW'---' $BRIGHT_RED-- $WHITE*$RESET                       "
    "  $WHITE*$BRIGHT_RED   /$BRIGHT_YELLOW  |  \\$BRIGHT_RED   $WHITE*$RESET                       "
)

WriteFrame "clear-day" 4 @(
    "        $BRIGHT_YELLOW    /   \\    $RESET                  "
    "        $BRIGHT_YELLOW   /     \\   $RESET                  "
    "        $BOLD_YELLOW   .---.    $RESET                  "
    "      $BRIGHT_RED -$BOLD_YELLOW(  *  )$BRIGHT_RED-      $RESET                  "
    "        $BOLD_YELLOW   '---'    $RESET                  "
    "        $BRIGHT_YELLOW   \\     /   $RESET                  "
)

# ---------- clear-night: crescent moon + twinkling stars ----------

WriteFrame "clear-night" 1 @(
    "  $WHITE*$RESET            $WHITE.$RESET                     $WHITE*$RESET     "
    "         $BRIGHT_YELLOW  .--.  $RESET     $WHITE.$RESET            "
    "  $WHITE.$RESET      $BRIGHT_YELLOW .'    '.$RESET                   "
    "         $BRIGHT_YELLOW(  )    )$RESET    $WHITE*$RESET              "
    "         $BRIGHT_YELLOW '.    .'$RESET             $WHITE.$RESET     "
    "  $WHITE*$RESET        $BRIGHT_YELLOW'--'  $RESET    $WHITE.$RESET            "
)

WriteFrame "clear-night" 2 @(
    "  $WHITE.$RESET            $WHITE*$RESET                     $WHITE.$RESET     "
    "         $BRIGHT_YELLOW  .--.  $RESET     $WHITE*$RESET            "
    "  $WHITE*$RESET      $BRIGHT_YELLOW .'    '.$RESET                   "
    "         $BRIGHT_YELLOW(  )    )$RESET    $WHITE.$RESET              "
    "         $BRIGHT_YELLOW '.    .'$RESET             $WHITE*$RESET     "
    "  $WHITE.$RESET        $BRIGHT_YELLOW'--'  $RESET    $WHITE*$RESET            "
)

WriteFrame "clear-night" 3 @(
    "  $WHITE*$RESET            $WHITE*$RESET                     $WHITE*$RESET     "
    "         $BRIGHT_YELLOW  .--.  $RESET     $WHITE.$RESET            "
    "  $WHITE*$RESET      $BRIGHT_YELLOW .'    '.$RESET                   "
    "         $BRIGHT_YELLOW(  )    )$RESET    $WHITE*$RESET              "
    "         $BRIGHT_YELLOW '.    .'$RESET             $WHITE*$RESET     "
    "  $WHITE*$RESET        $BRIGHT_YELLOW'--'  $RESET    $WHITE.$RESET            "
)

# ---------- partly-cloudy-day: sun + drifting cloud ----------

WriteFrame "partly-cloudy-day" 1 @(
    "      $BRIGHT_YELLOW \\ | /$RESET                          "
    "      $BRIGHT_YELLOW.-' '.$RESET    $WHITE  .--.  $RESET            "
    "    $BRIGHT_YELLOW-(     )$RESET $WHITE .-(    ).$RESET            "
    "      $BRIGHT_YELLOW'.   .'$RESET $WHITE(__________)$RESET           "
    "      $BRIGHT_YELLOW / | \\$RESET                            "
    "                                        "
)

WriteFrame "partly-cloudy-day" 2 @(
    "      $BRIGHT_YELLOW \\ | /$RESET                           "
    "      $BRIGHT_YELLOW.-' '.$RESET     $WHITE  .--.  $RESET           "
    "    $BRIGHT_YELLOW-(     )$RESET  $WHITE .-(    ).$RESET           "
    "      $BRIGHT_YELLOW'.   .'$RESET  $WHITE(__________)$RESET          "
    "      $BRIGHT_YELLOW / | \\$RESET                            "
    "                                        "
)

WriteFrame "partly-cloudy-day" 3 @(
    "      $BRIGHT_YELLOW \\ | /$RESET                            "
    "      $BRIGHT_YELLOW.-' '.$RESET      $WHITE  .--.  $RESET          "
    "    $BRIGHT_YELLOW-(     )$RESET   $WHITE .-(    ).$RESET          "
    "      $BRIGHT_YELLOW'.   .'$RESET   $WHITE(__________)$RESET         "
    "      $BRIGHT_YELLOW / | \\$RESET                            "
    "                                        "
)

# ---------- partly-cloudy-night ----------

WriteFrame "partly-cloudy-night" 1 @(
    "  $WHITE*$RESET                                     "
    "       $BRIGHT_YELLOW.-.$RESET     $GRAY  .--.  $RESET             "
    "      $BRIGHT_YELLOW(   )$RESET $GRAY  .-(    ).$RESET             "
    "       $BRIGHT_YELLOW'-'$RESET  $GRAY(__________)$RESET           "
    "                                  $WHITE.$RESET      "
    "                                        "
)

WriteFrame "partly-cloudy-night" 2 @(
    "  $WHITE.$RESET                                     "
    "       $BRIGHT_YELLOW.-.$RESET      $GRAY  .--.  $RESET            "
    "      $BRIGHT_YELLOW(   )$RESET  $GRAY .-(    ).$RESET            "
    "       $BRIGHT_YELLOW'-'$RESET   $GRAY(__________)$RESET          "
    "                                  $WHITE*$RESET      "
    "                                        "
)

WriteFrame "partly-cloudy-night" 3 @(
    "  $WHITE*$RESET                                     "
    "       $BRIGHT_YELLOW.-.$RESET       $GRAY  .--.  $RESET           "
    "      $BRIGHT_YELLOW(   )$RESET   $GRAY .-(    ).$RESET           "
    "       $BRIGHT_YELLOW'-'$RESET    $GRAY(__________)$RESET         "
    "                                  $WHITE.$RESET      "
    "                                        "
)

# ---------- cloudy: overlapping clouds drifting ----------

WriteFrame "cloudy" 1 @(
    "                                        "
    "    $WHITE  .--.        .--.            $RESET   "
    "    $WHITE.-(    ).   .-(    ).         $RESET   "
    "    $WHITE(________) (_________).       $RESET   "
    "                                        "
    "                                        "
)

WriteFrame "cloudy" 2 @(
    "                                        "
    "    $WHITE   .--.        .--.           $RESET   "
    "    $WHITE .-(    ).   .-(    ).        $RESET   "
    "    $WHITE (________) (_________).      $RESET   "
    "                                        "
    "                                        "
)

WriteFrame "cloudy" 3 @(
    "                                        "
    "    $WHITE    .--.        .--.          $RESET   "
    "    $WHITE  .-(    ).   .-(    ).       $RESET   "
    "    $WHITE  (________) (_________).     $RESET   "
    "                                        "
    "                                        "
)

# ---------- fog: layered mist ----------

WriteFrame "fog" 1 @(
    "                                        "
    "  $GRAY~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~$RESET"
    "  $GRAY  ~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~$RESET"
    "  $GRAY~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~$RESET"
    "  $GRAY    ~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~$RESET"
    "                                        "
)

WriteFrame "fog" 2 @(
    "                                        "
    "  $GRAY ~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~$RESET"
    "  $GRAY    ~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~$RESET"
    "  $GRAY  ~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~$RESET"
    "  $GRAY      ~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~$RESET"
    "                                        "
)

WriteFrame "fog" 3 @(
    "                                        "
    "  $GRAY  ~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~$RESET"
    "  $GRAY~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~$RESET"
    "  $GRAY    ~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~$RESET"
    "  $GRAY  ~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~$RESET"
    "                                        "
)

# ---------- drizzle: light rain ----------

WriteFrame "drizzle" 1 @(
    "    $GRAY  .--.  $RESET                            "
    "    $GRAY.-(    ).$RESET                           "
    "    $GRAY(__________)$RESET                        "
    "      $BLUE.   .   .   .   $RESET                 "
    "        $BLUE.   .   .   .$RESET                  "
    "                                        "
)

WriteFrame "drizzle" 2 @(
    "    $GRAY  .--.  $RESET                            "
    "    $GRAY.-(    ).$RESET                           "
    "    $GRAY(__________)$RESET                        "
    "        $BLUE.   .   .   .$RESET                  "
    "      $BLUE.   .   .   .   $RESET                 "
    "                                        "
)

WriteFrame "drizzle" 3 @(
    "    $GRAY  .--.  $RESET                            "
    "    $GRAY.-(    ).$RESET                           "
    "    $GRAY(__________)$RESET                        "
    "                                        "
    "      $BLUE.   .   .   .   $RESET                 "
    "        $BLUE.   .   .   .$RESET                  "
)

# ---------- rain: drops visibly falling row-by-row across frames ----------
# Cloud at the top stays put; the streak rows below cycle through 3 column-offset patterns
# so the eye sees the drops "fall" in a clear loop. Bright cyan accents on a small subset
# of drops make the front-of-storm pop without losing the dim blue body of the rain.

WriteFrame "rain" 1 @(
    "    $GRAY  .---.   .---.  $RESET                   "
    "    $GRAY.-(     ).(     ).$RESET                  "
    "    $GRAY(___________________)$RESET               "
    "      $BLUE| | | | | | | |$RESET                  "
    "                                        "
    "                                        "
)

WriteFrame "rain" 2 @(
    "    $GRAY  .---.   .---.  $RESET                   "
    "    $GRAY.-(     ).(     ).$RESET                  "
    "    $GRAY(___________________)$RESET               "
    "       $BLUE\\ \\ \\ \\ \\ \\ \\ \\$RESET                 "
    "      $BRIGHT_CYAN' ' ' ' ' ' ' '$RESET                  "
    "                                        "
)

WriteFrame "rain" 3 @(
    "    $GRAY  .---.   .---.  $RESET                   "
    "    $GRAY.-(     ).(     ).$RESET                  "
    "    $GRAY(___________________)$RESET               "
    "      $BLUE| | | | | | | |$RESET                  "
    "       $BLUE\\ \\ \\ \\ \\ \\ \\ \\$RESET                 "
    "      $BRIGHT_CYAN' ' ' ' ' ' ' '$RESET                  "
)

WriteFrame "rain" 4 @(
    "    $GRAY  .---.   .---.  $RESET                   "
    "    $GRAY.-(     ).(     ).$RESET                  "
    "    $GRAY(___________________)$RESET               "
    "       $BLUE\\ \\ \\ \\ \\ \\ \\ \\$RESET                 "
    "      $BLUE| | | | | | | |$RESET                  "
    "       $BRIGHT_CYAN. . . . . . . . $RESET                 "
)

# ---------- snow: flakes drifting in a clear zigzag ----------
# Three falling rows, each frame shifts them by +/- 1 col in opposite directions so the
# eye reads it as snow drifting through wind. Mix * and . glyphs across rows to mimic
# different flake sizes.

WriteFrame "snow" 1 @(
    "    $WHITE  .---.   .---.  $RESET                  "
    "    $WHITE.-(     ).(     ).$RESET                 "
    "    $WHITE(___________________)$RESET              "
    "       $BRIGHT_WHITE_BOLD*   .   *   .   *$RESET                "
    "      $BRIGHT_WHITE_BOLD.   *   .   *   . $RESET                "
    "       $BRIGHT_WHITE_BOLD*   .   *   .   *$RESET                "
)

WriteFrame "snow" 2 @(
    "    $WHITE  .---.   .---.  $RESET                  "
    "    $WHITE.-(     ).(     ).$RESET                 "
    "    $WHITE(___________________)$RESET              "
    "        $BRIGHT_WHITE_BOLD.   *   .   *   .$RESET               "
    "       $BRIGHT_WHITE_BOLD*   .   *   .   *$RESET                "
    "        $BRIGHT_WHITE_BOLD.   *   .   *   .$RESET               "
)

WriteFrame "snow" 3 @(
    "    $WHITE  .---.   .---.  $RESET                  "
    "    $WHITE.-(     ).(     ).$RESET                 "
    "    $WHITE(___________________)$RESET              "
    "         $BRIGHT_WHITE_BOLD*   .   *   .   *$RESET              "
    "        $BRIGHT_WHITE_BOLD.   *   .   *   .$RESET               "
    "         $BRIGHT_WHITE_BOLD*   .   *   .   *$RESET              "
)

WriteFrame "snow" 4 @(
    "    $WHITE  .---.   .---.  $RESET                  "
    "    $WHITE.-(     ).(     ).$RESET                 "
    "    $WHITE(___________________)$RESET              "
    "        $BRIGHT_WHITE_BOLD*   .   *   .   *$RESET               "
    "       $BRIGHT_WHITE_BOLD.   *   .   *   . $RESET                "
    "        $BRIGHT_WHITE_BOLD*   .   *   .   *$RESET               "
)

# ---------- thunderstorm: dark cloud + lightning bolt that flashes every other frame ----------
# Frames alternate dim/dim/flash/dim so the lightning reads as an intermittent strike rather
# than a steady glow.

WriteFrame "thunderstorm" 1 @(
    "    $DARKGRAY  .---.   .---.  $RESET                "
    "    $DARKGRAY.-(     ).(     ).$RESET               "
    "    $DARKGRAY(___________________)$RESET            "
    "       $BLUE\\ \\ \\ \\ \\ \\ \\ \\ $RESET                "
    "       $BLUE \\ \\ \\ \\ \\ \\ \\ \\$RESET                "
    "       $BLUE\\ \\ \\ \\ \\ \\ \\ \\ $RESET                "
)

WriteFrame "thunderstorm" 2 @(
    "    $DARKGRAY  .---.   .---.  $RESET                "
    "    $DARKGRAY.-(     ).(     ).$RESET               "
    "    $BOLD_YELLOW(___________________)$RESET         "
    "       $BLUE\\ \\$BOLD_YELLOW   /$BLUE \\ \\ \\ \\         $RESET           "
    "       $BLUE \\$BOLD_YELLOW  /__$BLUE \\ \\ \\ \\        $RESET           "
    "       $BLUE\\ $BOLD_YELLOW   /$BLUE \\ \\ \\ \\ \\         $RESET          "
)

WriteFrame "thunderstorm" 3 @(
    "    $DARKGRAY  .---.   .---.  $RESET                "
    "    $DARKGRAY.-(     ).(     ).$RESET               "
    "    $DARKGRAY(___________________)$RESET            "
    "       $BLUE \\ \\ \\ \\ \\ \\ \\ \\$RESET                "
    "       $BLUE\\ \\ \\ \\ \\ \\ \\ \\ $RESET                "
    "       $BLUE \\ \\ \\ \\ \\ \\ \\ \\$RESET                "
)

WriteFrame "thunderstorm" 4 @(
    "    $DARKGRAY  .---.   .---.  $RESET                "
    "    $DARKGRAY.-(     ).(     ).$RESET               "
    "    $BOLD_YELLOW(___________________)$RESET         "
    "       $BLUE\\ \\ \\$BOLD_YELLOW /$BLUE \\ \\ \\ \\         $RESET           "
    "       $BLUE \\ \\$BOLD_YELLOW /_$BLUE \\ \\ \\ \\        $RESET            "
    "       $BLUE\\ \\$BOLD_YELLOW /$BLUE \\ \\ \\ \\ \\         $RESET            "
)

# ---------- default: generic mild scene (used when condition has no folder) ----------

WriteFrame "default" 1 @(
    "                                        "
    "      $WHITE  .--.  $RESET                          "
    "      $WHITE.-(    ).$RESET                         "
    "      $WHITE(__________)$RESET                      "
    "                                        "
    "  $GRAY~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~$RESET"
)

WriteFrame "default" 2 @(
    "                                        "
    "       $WHITE  .--.  $RESET                         "
    "       $WHITE.-(    ).$RESET                        "
    "       $WHITE(__________)$RESET                     "
    "                                        "
    "  $GRAY~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~$RESET"
)

WriteFrame "default" 3 @(
    "                                        "
    "        $WHITE  .--.  $RESET                        "
    "        $WHITE.-(    ).$RESET                       "
    "        $WHITE(__________)$RESET                    "
    "                                        "
    "  $GRAY~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~~$RESET"
)

Write-Host "`nDone. Frames generated under $OutputRoot"
