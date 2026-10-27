import { useState } from "react";
import {
  ANTHROPIC_MODELS,
  formFromSettings,
  inputFromSettings,
  isDirty,
  type LlmFormErrors,
  type LlmFormValues,
  toLlmInput,
  validateLlmForm,
  withProvider,
} from "@/features/llm-settings/model/form";
import { ApiKeyField } from "@/features/llm-settings/ui/api-key-field";
import { AnthropicFields, OpenAiFields } from "@/features/llm-settings/ui/provider-fields";
import { TestResultView } from "@/features/llm-settings/ui/test-result";
import { WhatIsSent } from "@/features/llm-settings/ui/what-is-sent";
import { type LLMProvider, type LLMSettings, useLlmModels, useSaveLlmSettings, useTestLlm } from "@/shared/api";
import { announce } from "@/shared/lib/announcer";
import { Button } from "@/shared/ui/button";
import { DialogFooter } from "@/shared/ui/dialog";
import { SegmentedControl } from "@/shared/ui/segmented-control";
import { StatusIcon } from "@/shared/ui/status-icon";

const PROVIDERS: { value: LLMProvider; label: string }[] = [
  { value: "none", label: "None" },
  { value: "anthropic", label: "Anthropic" },
  { value: "openai_compat", label: "OpenAI-compatible" },
];

function useModelList(saved: LLMSettings, values: LlmFormValues) {
  const anthropicSaved = saved.provider === "anthropic" && values.provider === "anthropic";
  const models = useLlmModels(anthropicSaved);
  const anthropicModels = anthropicSaved && models.data?.length ? models.data : ANTHROPIC_MODELS;
  const sameEndpoint =
    saved.provider === "openai_compat" &&
    values.provider === "openai_compat" &&
    saved.baseUrl === values.baseUrl.trim();
  return {
    models,
    anthropicModels,
    loadBlocked: sameEndpoint ? undefined : "Save the base URL first; models are listed from the saved endpoint.",
    openAiModels: values.provider === "openai_compat" && sameEndpoint ? models.data : undefined,
  };
}

export function LlmSettingsForm({ saved }: { saved: LLMSettings }) {
  const [values, setValues] = useState(() => formFromSettings(saved));
  const [errors, setErrors] = useState<LlmFormErrors>({});
  const save = useSaveLlmSettings();
  const removeKey = useSaveLlmSettings();
  const test = useTestLlm();
  const list = useModelList(saved, values);
  const dirty = isDirty(values, saved);
  const keySaved = saved.hasApiKey && saved.provider === values.provider;

  const change = <K extends keyof LlmFormValues>(field: K, value: LlmFormValues[K]) => {
    setValues((v) => ({ ...v, [field]: value }));
    setErrors((e) => ({ ...e, [field]: undefined }));
  };

  const onSave = () => {
    const found = validateLlmForm(values);
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    test.reset();
    save.mutate(toLlmInput(values), {
      onSuccess: (next) => {
        // The typed key is never kept in the browser once the vault has it.
        setValues({ ...formFromSettings(next), apiKey: "" });
        announce("Assistant settings saved.");
      },
    });
  };

  const onRemoveKey = () =>
    removeKey.mutate(
      { ...inputFromSettings(saved), apiKey: "" },
      { onSuccess: () => announce("API key removed from the vault.") },
    );

  const onLoadModels = () =>
    void list.models.refetch().then((r) => r.data && announce(`Loaded ${r.data.length} models.`));

  return (
    <form
      className="flex flex-col gap-4"
      onSubmit={(e) => {
        e.preventDefault();
        onSave();
      }}
    >
      <div className="flex flex-col gap-1">
        <span id="llm-provider-label" className="text-xs font-medium">
          Provider
        </span>
        <SegmentedControl
          aria-labelledby="llm-provider-label"
          options={PROVIDERS}
          value={values.provider}
          onValueChange={(p) => {
            setValues((v) => withProvider(v, p));
            setErrors({});
          }}
        />
      </div>

      {values.provider === "anthropic" && (
        <AnthropicFields values={values} onChange={change} models={list.anthropicModels} />
      )}
      {values.provider === "openai_compat" && (
        <OpenAiFields
          values={values}
          errors={errors}
          onChange={change}
          models={list.openAiModels}
          onLoadModels={onLoadModels}
          loadingModels={list.models.isFetching}
          loadBlocked={list.loadBlocked}
        />
      )}
      {list.models.isError && values.provider === "openai_compat" && !list.loadBlocked && (
        <p role="alert" className="flex items-start gap-1 text-xs text-status-danger-fg">
          <StatusIcon tone="danger" className="size-3.5" />
          Could not list models: {list.models.error.message}
        </p>
      )}
      {values.provider !== "none" && (
        <ApiKeyField
          value={values.apiKey}
          onChange={(v) => change("apiKey", v)}
          saved={keySaved}
          optional={values.provider === "openai_compat"}
          error={errors.apiKey}
          onRemove={onRemoveKey}
          removing={removeKey.isPending}
        />
      )}

      <WhatIsSent values={values} />

      {save.isError && (
        <p role="alert" className="flex items-start gap-1 text-xs text-status-danger-fg">
          <StatusIcon tone="danger" className="size-3.5" />
          {save.error.message}
        </p>
      )}
      <TestResultView result={test.data} error={test.error} />

      <DialogFooter className="items-center">
        {dirty && saved.provider !== "none" && (
          <span className="mr-auto text-xs text-text-muted">Save to test these settings.</span>
        )}
        <Button
          type="button"
          variant="outline"
          disabled={dirty || saved.provider === "none" || test.isPending}
          onClick={() => test.mutate()}
        >
          {test.isPending ? "Testing…" : "Test"}
        </Button>
        <Button type="submit" disabled={save.isPending || !dirty}>
          {save.isPending ? "Saving…" : "Save"}
        </Button>
      </DialogFooter>
    </form>
  );
}
