FROM node:22-alpine AS build
WORKDIR /app
COPY package.json ./
COPY apps/api ./apps/api
RUN npm install --omit=dev

FROM node:22-alpine
WORKDIR /app
COPY --from=build /app ./
CMD ["node", "apps/api/src/server.js"]
