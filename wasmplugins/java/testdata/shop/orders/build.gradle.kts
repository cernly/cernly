plugins {
    java
    id("org.springframework.boot") version "3.3.5"
}

val feignVersion = "13.5"

dependencies {
    implementation(libs.bundles.web)
    implementation(libs.liquibase.core)
    runtimeOnly(libs.postgresql)
    implementation(project(":billing"))
    implementation("io.github.openfeign:feign-core:$feignVersion")
    compileOnly("org.projectlombok:lombok:1.18.34")
    testImplementation(platform("org.junit:junit-bom:5.11.3"))
    testImplementation(libs.junit.jupiter)
}
