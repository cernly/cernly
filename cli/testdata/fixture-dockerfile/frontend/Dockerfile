FROM node:26.12.1-alpine AS deps
WORKDIR /app
COPY package.json package-lock.json ./
COPY packages/web-ui/package.json packages/web-ui/
RUN npm ci --workspace=frontend

FROM node:26.12.1-alpine AS builder
WORKDIR /app
COPY --from=deps /app/node_modules ./node_modules
COPY packages/web-ui ./packages/web-ui
COPY frontend ./frontend
RUN npm run build --workspace=frontend

FROM node:26.12.1-alpine
WORKDIR /app
COPY --from=builder /app/frontend/.next ./.next
CMD ["node", "server.js"]
