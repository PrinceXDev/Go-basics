import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: "Ad Click Analytics",
  description: "Near real-time ad click analytics",
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en">
      <body>{children}</body>
    </html>
  );
}
