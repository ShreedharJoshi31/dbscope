import { LoaderCircle } from "lucide-react";
import { useLlmSettingsDialog } from "@/features/llm-settings/model/dialog-store";
import { LlmSettingsForm } from "@/features/llm-settings/ui/llm-settings-form";
import { useLlmSettings } from "@/shared/api";
import { Button } from "@/shared/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/shared/ui/dialog";
import { StatusIcon } from "@/shared/ui/status-icon";

function Body() {
  const settings = useLlmSettings();
  if (settings.isPending) {
    return (
      <p role="status" className="flex items-center gap-2 text-sm text-text-muted">
        <LoaderCircle aria-hidden className="size-4 animate-spin" />
        Loading settings…
      </p>
    );
  }
  if (settings.isError) {
    const locked = settings.error.code === "secrets_locked";
    return (
      <div role="alert" className="flex flex-col items-start gap-2 text-sm">
        <p className="flex items-center gap-1.5 font-medium text-status-danger-fg">
          <StatusIcon tone="danger" />
          {locked ? "Unlock the secrets vault to change the assistant settings" : "Could not load the settings"}
        </p>
        <p className="text-text-muted">{settings.error.message}</p>
        <Button variant="outline" size="sm" onClick={() => void settings.refetch()}>
          Try again
        </Button>
      </div>
    );
  }
  return <LlmSettingsForm saved={settings.data} />;
}

/** Global LLM settings (docs/API.md "LLM settings"); reachable from the palette and the assistant. */
export function LlmSettingsDialog() {
  const { open, setOpen } = useLlmSettingsDialog();
  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>Assistant model</DialogTitle>
          <DialogDescription>
            Choose the model that turns questions into SQL. Bring your own Anthropic key, or point dbscope at an
            OpenAI-compatible server such as Ollama.
          </DialogDescription>
        </DialogHeader>
        {open && <Body />}
      </DialogContent>
    </Dialog>
  );
}
