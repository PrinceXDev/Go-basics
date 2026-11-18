// The browser calls /api/* on the Next.js origin and Next proxies it to the
// Go API, so there is no CORS setup and no API address in client code.
const API_URL = process.env.API_URL ?? "http://localhost:8080";

/** @type {import('next').NextConfig} */
const nextConfig = {
  async rewrites() {
    return [{ source: "/api/:path*", destination: `${API_URL}/api/:path*` }];
  },
};

export default nextConfig;
