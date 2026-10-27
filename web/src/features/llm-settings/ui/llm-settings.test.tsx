import { screen, waitFor, within } from "@testing-library/react";
import { HttpResponse, http } from "msw/http";
import { afterEach, describe, expect, it } from "vitest";
import {
  formFromSettings,
  isDirty,
  LlmSettingsDialog,
  toLlmInput,
  useLlmSettingsDialog,
  validateLlmForm,
  withProvider,
} from "@/features/llm-settings";
import type { LLMSettings, LLMSettingsInput } from "@/shared/api";
import { makeProfile, renderWithProviders } from "@/test/render";
import { server } from "@/test/server";

const SAVED: LLMSettings = {
  provider: "anthropic",
  model: "claude-opus-5-5",
  summaryModel: "claude-haiku-5-5",
  effort: "medium",
  hasApiKey: true,
};
const KEY = "sk-ant-secret-0123456789";

function serve(initial: LLMSettings) {
  let settings = initial;
  const puts: LLMSettingsInput[] = [];
  server.use(
    http.get("/api/profiles", () => HttpResponse.json([makeProfile({ summaryMode: "rows", maxRowsToLLM: 50 })])),
    http.get("/api/llm/settings", () => HttpResponse.json(settings)),
    http.get("/api/llm/models", () =>
      HttpResponse.json(
        settings.provider === "anthropic"
          ? ["claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5"]
          : ["llama3.1:8b"],
      ),
    ),
    http.put("/api/llm/settings", async ({ request }) => {
      const body = (await request.json()) as LLMSettingsInput;
      puts.push(body);
      const hasApiKey =
        body.apiKey === undefined ? settings.hasApiKey && settings.provider === body.provider : body.apiKey !== "";
      const { apiKey: _, ...rest } = body;
      settings = { ...rest, hasApiKey } as LLMSettings;
      return HttpResponse.json(settings);
    }),
    http.post("/api/llm/test", () =>
      HttpResponse.json({
        ok: false,
        provider: "anthropic",
        model: settings.model,
        latencyMs: 80,
        error: { kind: "auth", message: "invalid x-api-key" },
      }),
    ),
  );
  return { puts, current: () => settings };
}

function open() {
  useLlmSettingsDialog.setState({ open: true });
  return renderWithProviders(<LlmSettingsDialog />);
}

afterEach(() => useLlmSettingsDialog.setState({ open: false }));

describe("LLM settings form model", () => {
  it("sends the key only when typed, and switching provider resets the other provider's fields", () => {
    const values = formFromSettings(SAVED);
    expect(values.apiKey).toBe("");
    expect(toLlmInput(values)).toEqual({
      provider: "anthropic",
      model: "claude-opus-5-5",
      summaryModel: "claude-haiku-5-5",
      effort: "medium",
    });
    expect(toLlmInput({ ...values, apiKey: KEY }).apiKey).toBe(KEY);
    const ollama = withProvider(values, "openai_compat");
    expect(ollama).toMatchObject({ provider: "openai_compat", model: "", baseUrl: "http://localhost:11434/v1/" });
    expect(validateLlmForm(ollama).model).toBeDefined();
    expect(validateLlmForm({ ...ollama, model: "llama3.1:8b", baseUrl: "http://u:p@host/v1/" }).baseUrl).toBeDefined();
    expect(validateLlmForm({ ...ollama, model: "llama3.1:8b" })).toEqual({});
    expect(toLlmInput({ ...ollama, model: "llama3.1:8b" })).toEqual({
      provider: "openai_compat",
      model: "llama3.1:8b",
      summaryModel: "",
      baseUrl: "http://localhost:11434/v1/",
    });
    expect(validateLlmForm({ ...values, apiKey: "a\nb" }).apiKey).toBeDefined();
    expect(isDirty(values, SAVED)).toBe(false);
    expect(isDirty({ ...values, apiKey: "x" }, SAVED)).toBe(true);
  });
});

describe("LlmSettingsDialog", () => {
  it("never shows a stored key: it says saved, and the field stays empty", async () => {
    serve(SAVED);
    open();
    const field = await screen.findByLabelText("API key");
    expect(field).toHaveValue("");
    expect(field).toHaveAttribute("type", "password");
    expect(screen.getByTestId("api-key-saved")).toHaveTextContent("Key saved");
    expect(screen.getByRole("button", { name: "Remove key" })).toBeInTheDocument();
    expect(document.body.textContent).not.toContain(KEY);
  });

  it("saves a typed key once, clears it from the form and reports hasApiKey", async () => {
    const stub = serve({ ...SAVED, hasApiKey: false });
    const { user } = open();
    expect(screen.queryByTestId("api-key-saved")).not.toBeInTheDocument();
    await user.type(await screen.findByLabelText("API key"), KEY);
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(stub.puts).toHaveLength(1));
    expect(stub.puts[0]).toMatchObject({ provider: "anthropic", apiKey: KEY });
    expect(await screen.findByTestId("api-key-saved")).toBeInTheDocument();
    expect(screen.getByLabelText("API key")).toHaveValue("");
    expect((screen.getByLabelText("API key") as HTMLInputElement).value).not.toContain("sk-");
  });

  it('Remove key sends apiKey "" with the saved settings', async () => {
    const stub = serve(SAVED);
    const { user } = open();
    await user.click(await screen.findByRole("button", { name: "Remove key" }));
    await waitFor(() => expect(stub.puts).toHaveLength(1));
    expect(stub.puts[0]).toEqual({
      provider: "anthropic",
      model: "claude-opus-5-5",
      summaryModel: "claude-haiku-5-5",
      effort: "medium",
      apiKey: "",
    });
    await waitFor(() => expect(screen.queryByTestId("api-key-saved")).not.toBeInTheDocument());
  });

  it("Test shows human copy for the error kind", async () => {
    serve(SAVED);
    const { user } = open();
    await user.click(await screen.findByRole("button", { name: "Test" }));
    const result = await screen.findByTestId("llm-test-result");
    expect(result).toHaveTextContent("The provider rejected the API key");
    expect(result).toHaveTextContent("invalid x-api-key");
  });

  it("states what leaves the machine, per the active connection's summary mode", async () => {
    serve(SAVED);
    open();
    const box = await screen.findByRole("region", { name: /What is sent to Anthropic/ });
    expect(box).toHaveTextContent("schema of the relevant tables");
    await waitFor(() => expect(box).toHaveTextContent("For orders-dev: up to 50 result rows"));
  });

  it("configures an OpenAI-compatible endpoint, then loads its models from the saved endpoint", async () => {
    const stub = serve({ provider: "none", model: "", summaryModel: "", hasApiKey: false });
    const { user } = open();
    await user.click(await screen.findByRole("radio", { name: "OpenAI-compatible" }));
    expect(screen.getByRole("button", { name: "Load models" })).toBeDisabled();
    await user.type(screen.getByLabelText("Model"), "llama3.1:8b");
    expect(
      screen.getByRole("region", { name: /What is sent to http:\/\/localhost:11434\/v1\/, a server on this machine/ }),
    ).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() => expect(stub.current().provider).toBe("openai_compat"));
    expect(stub.puts[0]).not.toHaveProperty("apiKey");
    const load = await screen.findByRole("button", { name: "Load models" });
    await waitFor(() => expect(load).toBeEnabled());
    await user.click(load);
    expect(await screen.findByText("1 models available; pick one in the Model field.")).toBeInTheDocument();
    expect(within(document.body).queryByTestId("api-key-saved")).not.toBeInTheDocument();
  });
});
