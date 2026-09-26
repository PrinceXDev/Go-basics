import ServerCard from "./ServerCard";
import { useServerFeed } from "./useServerFeed";
import "./App.css";

const CONNECTION_LABEL = {
  connecting: "Connecting…",
  open: "Live",
  closed: "Reconnecting…",
};

export default function App() {
  const { servers, connectionState } = useServerFeed();

  const healthyCount = servers.filter((s) => s.status === "healthy").length;

  return (
    <div className="page">
      <header className="page__header">
        <div>
          <h1>Server Fleet Dashboard</h1>
          <p className="page__subtitle">
            {servers.length} servers reporting &middot; {healthyCount} healthy
          </p>
        </div>
        <span className={`connection-pill connection-pill--${connectionState}`}>
          <span className="connection-pill__dot" />
          {CONNECTION_LABEL[connectionState]}
        </span>
      </header>

      <main className="server-grid">
        {servers.length === 0 ? (
          <p className="empty-state">Waiting for the first health report…</p>
        ) : (
          servers.map((server) => <ServerCard key={server.id} server={server} />)
        )}
      </main>
    </div>
  );
}
