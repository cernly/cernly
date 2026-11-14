package com.example.orders;

import org.springframework.beans.factory.annotation.Value;
import org.springframework.cloud.openfeign.FeignClient;
import org.springframework.web.client.RestTemplate;
import org.springframework.web.reactive.function.client.WebClient;

@FeignClient(name = "fraud", url = "https://fraud.example-risk.com")
interface FraudClient {}

@FeignClient(name = "ledger", url = "${LEDGER_URL}")
interface LedgerClient {}

class PaymentsClient {
    static final String RATES_URL = "https://rates.example-fx.com/latest";

    @Value("${PAYMENTS_API_KEY}")
    private String apiKey;

    @Value("${payments.timeout:5s}")
    private String timeout;

    private final WebClient web = WebClient.builder().baseUrl("https://api.payments.example-pay.io").build();
    private final RestTemplate rest = new RestTemplate();

    String rates() {
        String region = System.getenv("ORDERS_REGION");
        // "https://commented.example-fx.com" is not read
        return rest.getForObject(RATES_URL, String.class);
    }
}
