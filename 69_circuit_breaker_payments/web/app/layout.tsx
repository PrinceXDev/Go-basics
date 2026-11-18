import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: "Payment Circuit Breaker",
  description: "Circuit breaker, retry with backoff and async retry queue for a payment gateway",
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en">
      <body>{children}</body>
    </html>
  );
}
