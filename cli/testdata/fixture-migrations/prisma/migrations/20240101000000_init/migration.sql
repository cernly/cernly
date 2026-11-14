-- CreateEnum
CREATE TYPE "Plan" AS ENUM ('FREE', 'TEAM');

-- CreateTable
CREATE TABLE "Organisation" (
    "id" TEXT NOT NULL,
    "plan" "Plan" NOT NULL DEFAULT 'FREE',
    "createdAt" TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,

    CONSTRAINT "Organisation_pkey" PRIMARY KEY ("id")
);
