export async function load(): Promise<Response> {
  return fetch("https://shop.acme-widgets.be/api/products");
}
