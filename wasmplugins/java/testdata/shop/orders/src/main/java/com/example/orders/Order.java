package com.example.orders;

import jakarta.persistence.*;
import java.util.List;

/** An order. @Entity in a comment is not read. */
@Entity
@Table(name = "orders")
public class Order {
    @Id
    @GeneratedValue
    private Long id;

    @Column(name = "customer_email", nullable = false, unique = true)
    private String customerEmail;

    private long totalCents;

    @Enumerated(EnumType.STRING)
    private Status status;

    @ManyToOne
    @JoinColumn(name = "customer_id")
    private Customer customer;

    @OneToMany(mappedBy = "order")
    private List<OrderLine> lines;

    @Transient
    private String cache;

    private static final String KIND = "order";

    public Long getId() { return id; }

    public enum Status { OPEN, PAID }
}
