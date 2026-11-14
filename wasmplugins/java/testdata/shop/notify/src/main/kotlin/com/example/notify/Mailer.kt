package com.example.notify

import org.springframework.beans.factory.annotation.Value

const val MAIL_API_URL = "https://mail.example-send.com/v3"

class Mailer(@Value("\${NOTIFY_TOKEN}") private val token: String) {
    val client = WebClient.create(MAIL_API_URL)
}
