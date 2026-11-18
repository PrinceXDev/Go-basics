// The browser calls /api/* on the Next.js origin; Next proxies to the Go API.
// This avoids CORS and keeps the API address out of client code.
const API_URL = process.env.API_URL ?? "http://localhost:8080";

/** @type {import('next').NextConfig} */
const nextConfig = {
  output: "standalone",
  async rewrites() {
    return [{ source: "/api/:path*", destination: `${API_URL}/api/:path*` }];
  },
};

export default nextConfig;
