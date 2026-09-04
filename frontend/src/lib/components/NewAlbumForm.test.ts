import { fireEvent, render, waitFor } from "@testing-library/svelte";
import { describe, expect, it, vi } from "vitest";
import NewAlbumForm from "./NewAlbumForm.svelte";

describe("NewAlbumForm", () => {
  it("trims the name before creating an album", async () => {
    const onCreate = vi.fn().mockResolvedValue(undefined);
    const { getByRole } = render(NewAlbumForm, { props: { onCreate } });

    await fireEvent.input(getByRole("textbox", { name: "Name" }), {
      target: { value: "  Darkroom selects  " },
    });
    await fireEvent.click(getByRole("button", { name: "Create" }));

    expect(onCreate).toHaveBeenCalledWith("Darkroom selects");
  });

  it("cancels without creating an album", async () => {
    const onCreate = vi.fn();
    const onCancel = vi.fn();
    const { getByRole } = render(NewAlbumForm, {
      props: { onCreate, onCancel },
    });

    await fireEvent.click(getByRole("button", { name: "Cancel" }));

    expect(onCancel).toHaveBeenCalledOnce();
    expect(onCreate).not.toHaveBeenCalled();
  });

  it("disables both actions while creation is in progress", async () => {
    let finish!: () => void;
    const onCreate = vi.fn(
      () =>
        new Promise<void>((resolve) => {
          finish = resolve;
        }),
    );
    const { getByRole } = render(NewAlbumForm, {
      props: { onCreate, onCancel: vi.fn() },
    });

    await fireEvent.input(getByRole("textbox", { name: "Name" }), {
      target: { value: "Portfolio" },
    });
    await fireEvent.click(getByRole("button", { name: "Create" }));

    expect(
      getByRole("button", { name: "Creating…" }).hasAttribute("disabled"),
    ).toBe(true);
    expect(
      getByRole("button", { name: "Cancel" }).hasAttribute("disabled"),
    ).toBe(true);

    finish();
    await waitFor(() => {
      expect(
        getByRole("button", { name: "Create" }).hasAttribute("disabled"),
      ).toBe(true);
    });
  });

  it("limits album names to 200 characters", async () => {
    const { getByRole } = render(NewAlbumForm, {
      props: { onCreate: vi.fn().mockResolvedValue(undefined) },
    });
    const input = getByRole("textbox", { name: "Name" }) as HTMLInputElement;

    await fireEvent.input(input, { target: { value: "a".repeat(201) } });

    expect(input.value).toHaveLength(200);
  });
});
