import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, expect, it, vi } from "vite-plus/test";

import type { OwnerAuthentication } from "../application/auth";

import { Login } from "./login";
import { MotionProvider } from "./ui/motion";

beforeEach(() => {
  localStorage.clear();
  vi.stubGlobal("matchMedia", (query: string) => ({
    matches: !query.includes("prefers-color-scheme"),
    addEventListener: () => {},
    removeEventListener: () => {},
  }));
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

function setup() {
  const authentication = {
    signIn: vi.fn<OwnerAuthentication["signIn"]>().mockResolvedValue(),
    createCredential: vi
      .fn<OwnerAuthentication["createCredential"]>()
      .mockResolvedValue('{"id":"public"}'),
  };
  render(
    <MotionProvider>
      <Login authentication={authentication} />
    </MotionProvider>,
  );
  return authentication;
}

it("shares themes and requires an explicit authenticator action", async () => {
  const authentication = setup();
  expect(authentication.signIn).not.toHaveBeenCalled();
  fireEvent.click(screen.getByLabelText("Switch to Latte theme"));
  expect(document.documentElement.dataset.theme).toBe("latte");
  fireEvent.click(screen.getByLabelText("Switch to Mocha theme"));
  expect(document.documentElement.dataset.theme).toBe("mocha");
  fireEvent.click(screen.getByRole("button", { name: "Sign in" }));
  await waitFor(() => expect(authentication.signIn).toHaveBeenCalledOnce());
});

it("returns enrollment proof for the host command, clearing the pasted options", async () => {
  const authentication = setup();
  fireEvent.click(screen.getByText("Enroll from Hermes", { exact: true }));
  fireEvent.change(screen.getByLabelText("Options from Hermes"), {
    target: { value: '{"publicKey":{}}' },
  });
  fireEvent.click(screen.getByRole("button", { name: "Create device credential" }));
  await waitFor(() =>
    expect(screen.getByLabelText("Response to paste into Hermes")).toHaveProperty(
      "value",
      '{"id":"public"}',
    ),
  );
  expect(authentication.createCredential).toHaveBeenCalledWith('{"publicKey":{}}');
  expect(screen.getByLabelText("Options from Hermes")).toHaveProperty("value", "");
});

it("surfaces authenticator errors and cancellation for sign-in and enrollment", async () => {
  const authentication = setup();
  for (const error of [new Error("Authenticator unavailable"), "cancelled"]) {
    authentication.signIn.mockRejectedValueOnce(error);
    fireEvent.click(screen.getByRole("button", { name: "Sign in" }));
    expect(
      await screen.findByText(error instanceof Error ? error.message : "Sign-in failed."),
    ).toBeDefined();
  }
  fireEvent.click(screen.getByText("Enroll from Hermes", { exact: true }));
  fireEvent.change(screen.getByLabelText("Options from Hermes"), { target: { value: "options" } });
  for (const error of [new Error("Device is synced"), "cancelled"]) {
    authentication.createCredential.mockRejectedValueOnce(error);
    fireEvent.click(screen.getByRole("button", { name: "Create device credential" }));
    expect(
      await screen.findByText(
        error instanceof Error ? error.message : "Credential creation failed.",
      ),
    ).toBeDefined();
  }
});
