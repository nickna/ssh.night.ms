using Night.Ms.SshServer.Providers;

namespace Night.Ms.SshServer.Tests;

public class WeatherConditionMapperTests
{
    [Theory]
    [InlineData(0, true, WeatherCondition.ClearDay)]
    [InlineData(0, false, WeatherCondition.ClearNight)]
    [InlineData(1, true, WeatherCondition.ClearDay)]
    [InlineData(1, false, WeatherCondition.ClearNight)]
    [InlineData(2, true, WeatherCondition.PartlyCloudyDay)]
    [InlineData(2, false, WeatherCondition.PartlyCloudyNight)]
    [InlineData(3, true, WeatherCondition.Cloudy)]
    [InlineData(3, false, WeatherCondition.Cloudy)]
    [InlineData(45, true, WeatherCondition.Fog)]
    [InlineData(48, false, WeatherCondition.Fog)]
    [InlineData(51, true, WeatherCondition.Drizzle)]
    [InlineData(55, true, WeatherCondition.Drizzle)]
    [InlineData(57, true, WeatherCondition.Drizzle)]
    [InlineData(61, true, WeatherCondition.Rain)]
    [InlineData(65, true, WeatherCondition.Rain)]
    [InlineData(67, true, WeatherCondition.Rain)]
    [InlineData(80, true, WeatherCondition.Rain)]
    [InlineData(82, false, WeatherCondition.Rain)]
    [InlineData(71, true, WeatherCondition.Snow)]
    [InlineData(75, false, WeatherCondition.Snow)]
    [InlineData(77, true, WeatherCondition.Snow)]
    [InlineData(86, true, WeatherCondition.Snow)]
    [InlineData(95, true, WeatherCondition.Thunderstorm)]
    [InlineData(96, false, WeatherCondition.Thunderstorm)]
    [InlineData(99, true, WeatherCondition.Thunderstorm)]
    [InlineData(12345, true, WeatherCondition.Unknown)]
    public void Map_returns_expected_bucket(int wmo, bool isDay, WeatherCondition expected)
    {
        Assert.Equal(expected, WeatherConditionMapper.Map(wmo, isDay));
    }

    [Theory]
    [InlineData(WeatherCondition.ClearDay, "clear-day")]
    [InlineData(WeatherCondition.ClearNight, "clear-night")]
    [InlineData(WeatherCondition.PartlyCloudyDay, "partly-cloudy-day")]
    [InlineData(WeatherCondition.PartlyCloudyNight, "partly-cloudy-night")]
    [InlineData(WeatherCondition.Cloudy, "cloudy")]
    [InlineData(WeatherCondition.Fog, "fog")]
    [InlineData(WeatherCondition.Drizzle, "drizzle")]
    [InlineData(WeatherCondition.Rain, "rain")]
    [InlineData(WeatherCondition.Snow, "snow")]
    [InlineData(WeatherCondition.Thunderstorm, "thunderstorm")]
    [InlineData(WeatherCondition.Unknown, "default")]
    public void ToSlug_returns_expected_directory_name(WeatherCondition condition, string expected)
    {
        Assert.Equal(expected, condition.ToSlug());
    }
}
