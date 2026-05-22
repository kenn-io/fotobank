export type SSEMessage = { id: string; type: string; data: unknown };

const KNOWN_EVENTS = [
  "hello",
  "import.progress",
  "ai.tag.completed",
  "ai.caption.completed",
  "ai.embed.completed",
  "ai.embed.generation_activated",
  "share.status.changed",
  "ai.health.changed",
  "catchup-required",
] as const;

type EventSourceLike = {
  addEventListener(name: string, fn: (ev: MessageEvent) => void): void;
  close(): void;
};

export class EventsStore {
  lastEvent = $state<SSEMessage | null>(null);
  catchupRequired = $state(false);
  private es: EventSourceLike | null = null;
  private ctor: new (url: string) => EventSourceLike;

  constructor(opts: { EventSourceCtor?: new (url: string) => EventSourceLike } = {}) {
    this.ctor = opts.EventSourceCtor ?? (EventSource as never);
  }

  connect(url = "/api/v1/events") {
    if (this.es) return;
    this.es = new this.ctor(url);
    for (const name of KNOWN_EVENTS) {
      this.es.addEventListener(name, (ev) => {
        let data: unknown = ev.data;
        try { data = JSON.parse(ev.data); } catch { /* leave as string */ }
        this.lastEvent = { id: ev.lastEventId ?? "", type: name, data };
        if (name === "catchup-required") this.catchupRequired = true;
      });
    }
  }

  disconnect() {
    this.es?.close();
    this.es = null;
  }
}
