using System;
using Shop.Infrastructure;

var builder = WebApplication.CreateBuilder(args);

// Payments run through Mollie; docs at https://docs.vendor-docs.io/payments
var mollieKey = Environment.GetEnvironmentVariable("SHOP_MOLLIE_API_KEY");

builder.Services.AddHttpClient("rates", c => c.BaseAddress = new Uri("https://api.ratesvendor.io/v2/"));
builder.Services.AddShopInfrastructure(builder.Configuration);

var app = builder.Build();
app.Run();
