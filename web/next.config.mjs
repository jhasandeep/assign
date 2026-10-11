// The browser talks only to Next; /backend/* is proxied to the Go API, so there
// is no CORS to configure.
const apiUrl = process.env.API_URL ?? "http://localhost:8080";

/** @type {import('next').NextConfig} */
const nextConfig = {
  async rewrites() {
    return [{ source: "/backend/:path*", destination: `${apiUrl}/:path*` }];
  },
};

export default nextConfig;
