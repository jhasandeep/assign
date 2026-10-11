export const metadata = { title: "Sutra Mini" };

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en">
      <body style={{ fontFamily: "system-ui, sans-serif", margin: 0, padding: 24, background: "#fafafa", color: "#111" }}>
        {children}
      </body>
    </html>
  );
}
