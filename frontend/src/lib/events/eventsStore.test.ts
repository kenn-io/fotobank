import { describe, it, expect } from "vitest";
import { EventsStore, type SSEMessage } from "./eventsStore.svelte";

class FakeEventSource {
  static instances: FakeEventSource[] = [];
  url: string;
  onmessage: ((ev: MessageEvent) => void) | null = null;
  onerror: (() => void) | null = null;
  listeners: Record<string, ((ev: MessageEvent) => void)[]> = {};
  constructor(url: string) {
    this.url = url;
    FakeEventSource.instances.push(this);
  }
  addEventListener(name: string, fn: (ev: MessageEvent) => void) {
    (this.listeners[name] ??= []).push(fn);
  }
  fire(name: string, ev: MessageEvent) {
    (this.listeners[name] ?? []).forEach((f) => f(ev));
  }
  close() {}
}

describe("EventsStore", () => {
  it("captures the hello event", () => {
    const store = new EventsStore({ EventSourceCtor: FakeEventSource as never });
    store.connect();
    const inst = FakeEventSource.instances.at(-1)!;
    inst.fire("hello", new MessageEvent("hello", { data: '{"principal":"alice"}', lastEventId: "0" }));
    expect(store.lastEvent?.type).toBe("hello");
  });

  it("stores incoming events with type and id", () => {
    const store = new EventsStore({ EventSourceCtor: FakeEventSource as never });
    store.connect();
    const inst = FakeEventSource.instances.at(-1)!;
    inst.fire("import.progress", new MessageEvent("import.progress", { data: '{"processed":3}', lastEventId: "12" }));
    const msg = store.lastEvent as SSEMessage;
    expect(msg.type).toBe("import.progress");
    expect(msg.id).toBe("12");
  });
});
