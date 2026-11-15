using System;
using Shop.Infrastructure;

var builder = WebApplication.CreateBuilder(args);

// Payments run through Mollie; docs at https://docs.vendor-docs.io/payments
var mollieKey = Environment.GetEnvironmentVariable("SHOP_MOLLIE_API_KEY");

builder.Services.AddHttpClient("rates", c => c.BaseAddress = new Uri("https://api.ratesvendor.io/v2/"));
builder.Services.AddShopInfrastructure(builder.Configuration);

var app = builder.Build();

app.MapGet("/health", () => "ok").AllowAnonymous();

var api = app.MapGroup("/api").RequireAuthorization();
api.MapGet("/orders", ListOrders);
api.MapGet("/orders/{id:int}", GetOrder);

app.MapControllers();
app.MapRazorPages();

app.Run();

static IResult ListOrders() => Results.Ok();

static IResult GetOrder(int id) => Results.Ok(id);
