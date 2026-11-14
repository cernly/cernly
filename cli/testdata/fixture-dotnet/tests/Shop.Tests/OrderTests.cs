using Shop.Domain;
using Xunit;

namespace Shop.Tests;

public class OrderTests
{
    [Fact]
    public void NewOrderHasNoTotal()
    {
        Assert.Equal(0m, new Order().Total);
    }
}
