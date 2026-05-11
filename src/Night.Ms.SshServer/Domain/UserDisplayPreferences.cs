namespace Night.Ms.SshServer.Domain;

public enum TemperatureUnit
{
    Celsius = 0,
    Fahrenheit = 1,
    Both = 2,
}

public enum ClockFormat
{
    Hours24 = 0,
    Hours12 = 1,
}

public enum DateFormat
{
    Iso = 0,
    UsSlash = 1,
    EuSlash = 2,
}
