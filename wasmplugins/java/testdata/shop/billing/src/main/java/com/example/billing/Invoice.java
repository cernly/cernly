package com.example.billing;

import jakarta.persistence.Entity;
import jakarta.persistence.Id;

@Entity
public class Invoice {
    @Id
    private long id;
    private long amountCents;
}
