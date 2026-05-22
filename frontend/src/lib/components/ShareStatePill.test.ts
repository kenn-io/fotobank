import { render } from "@testing-library/svelte";
import { describe, it, expect } from "vitest";
import ShareStatePill from "./ShareStatePill.svelte";

const base = {
  uuid: "x",
  target_type: "media_set" as const,
  target_album_id: null,
  target_summary: null,
  grantee: { hub: "h", user_id: "u" },
  allow_download: false,
  label: "",
  created_at: "2026-04-28T00:00:00Z",
  expires_at: null,
  revoked_at: null,
  broker_attempts: 0,
  broker_last_error: "",
};

describe("ShareStatePill", () => {
  it("renders Active for active state", () => {
    const { getByLabelText, getByText } = render(ShareStatePill, {
      props: { scope: { ...base, broker_status: "active" as const } },
    });
    expect(getByLabelText("Active")).not.toBeNull();
    expect(getByText("Active")).not.toBeNull();
  });

  it("renders Expired override when expires_at is in the past", () => {
    const { getByLabelText } = render(ShareStatePill, {
      props: {
        scope: {
          ...base,
          broker_status: "active" as const,
          expires_at: "2020-01-01T00:00:00Z",
        },
      },
    });
    expect(getByLabelText("Expired")).not.toBeNull();
  });

  it("does NOT override to Expired when broker_status is revoked_remote", () => {
    const { getByLabelText } = render(ShareStatePill, {
      props: {
        scope: {
          ...base,
          broker_status: "revoked_remote" as const,
          expires_at: "2020-01-01T00:00:00Z",
        },
      },
    });
    expect(getByLabelText("Revoked")).not.toBeNull();
  });
});
