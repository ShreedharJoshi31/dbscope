import { CircleAlert, LoaderCircle, PlugZap } from "lucide-react";
import { type FormEvent, useEffect, useId, useRef, useState } from "react";
import {
  defaultStatementTimeoutMs,
  ENGINE_META,
  ENGINE_ORDER,
  summarizeTest,
  TestResultChecklist,
} from "@/entities/connection";
import {
  applyParsedUrl,
  type ConnectionField,
  type ConnectionFormValues,
  EMPTY_CONNECTION_FORM,
  firstInvalidField,
  formFromProfile,
  sshCapable,
  toProfileInput,
  validateConnectionForm,
} from "@/features/add-connection/model/form";
import { AdvancedFields } from "@/features/add-connection/ui/advanced-fields";
import { ConnectionFields } from "@/features/add-connection/ui/connection-fields";
import { ConnectionUrlField } from "@/features/add-connection/ui/connection-url-field";
import { SshFields } from "@/features/add-connection/ui/ssh-fields";
import {
  type ApiError,
  type Engine,
  type Env,
  type ParsedConnectionUrl,
  type Profile,
  type TestResult,
  useCreateProfile,
  useRetestSavedProfile,
  useTestUnsavedProfile,
  useUpdateProfile,
} from "@/shared/api";
import { announce } from "@/shared/lib/announcer";
import { Button } from "@/shared/ui/button";
import { SegmentedControl } from "@/shared/ui/segmented-control";

const ENGINE_OPTIONS = ENGINE_ORDER.map((engine) => ({ value: engine, label: ENGINE_META[engine].label }));

/** Fields whose change makes a previous test result stale. */
const CONNECTION_FIELDS: ReadonlySet<ConnectionField> = new Set([
  "engine",
  "path",
  "host",
  "port",
  "database",
  "user",
  "password",
  "sslMode",
  "sslRootCert",
  "ssh",
  "sshHost",
  "sshPort",
  "sshUser",
  "sshKeyFile",
  "sshPassphrase",
]);

interface ConnectionFormProps {
  /** Present when editing an existing connection. */
  profile?: Profile | undefined;
  submitLabel: string;
  onSaved: (profile: Profile) => void;
  onCancel?: () => void;
  autoFocus?: boolean;
}

function saveErrorMessage(error: ApiError): string {
  if (error.code === "secrets_locked") return "Unlock the secrets vault, then save again.";
  return error.detail ? `${error.message} ${error.detail}` : error.message;
}

export function ConnectionForm({ profile, submitLabel, onSaved, onCancel, autoFocus }: ConnectionFormProps) {
  const [values, setValues] = useState<ConnectionFormValues>(() =>
    profile ? formFromProfile(profile) : EMPTY_CONNECTION_FORM,
  );
  const [touched, setTouched] = useState<ReadonlySet<ConnectionField>>(new Set());
  const [submitted, setSubmitted] = useState(false);
  const [timeoutEdited, setTimeoutEdited] = useState(Boolean(profile));
  const [advancedOpen, setAdvancedOpen] = useState(false);
  const [testResult, setTestResult] = useState<TestResult | null>(null);
  const [formError, setFormError] = useState<string | null>(null);
  const [nameConflict, setNameConflict] = useState<string | null>(null);

  const urlRef = useRef<HTMLInputElement>(null);
  const engineLabelId = useId();
  const engineDescriptionId = useId();
  const fieldRefs = useRef<Partial<Record<ConnectionField, HTMLElement | null>>>({});
  const register = (field: ConnectionField) => (el: HTMLElement | null) => {
    fieldRefs.current[field] = el;
  };

  useEffect(() => {
    if (autoFocus) urlRef.current?.focus();
  }, [autoFocus]);

  const testUnsaved = useTestUnsavedProfile();
  const testSaved = useRetestSavedProfile();
  const create = useCreateProfile();
  const update = useUpdateProfile();
  const testing = testUnsaved.isPending || testSaved.isPending;
  const saving = create.isPending || update.isPending;

  const allErrors = validateConnectionForm(values);
  if (nameConflict && values.name.trim() === nameConflict) {
    allErrors.name = "A connection with this name already exists. Choose another name.";
  }
  const visibleError = (field: ConnectionField) => (submitted || touched.has(field) ? allErrors[field] : undefined);

  function change<K extends ConnectionField>(field: K, value: ConnectionFormValues[K]) {
    setValues((prev) => {
      const next = { ...prev, [field]: value };
      // Moving between engines moves an untouched default port with it.
      if (field === "engine") {
        const from = ENGINE_META[prev.engine].defaultPort;
        const to = ENGINE_META[value as Engine].defaultPort;
        if (to && (prev.port === "" || prev.port === String(from))) next.port = String(to);
      }
      if (field === "env" && !timeoutEdited) {
        next.statementTimeoutMs = String(defaultStatementTimeoutMs(value as Env));
      }
      return next;
    });
    if (field === "statementTimeoutMs") setTimeoutEdited(true);
    // The server's warnings depend on the env it tested with, so a new env needs a new test.
    if (CONNECTION_FIELDS.has(field) || field === "env") setTestResult(null);
    setFormError(null);
  }

  const blur = (field: ConnectionField) => setTouched((prev) => new Set(prev).add(field));

  /** Returns false and moves focus to the first problem when the form is invalid. */
  function ensureValid(): boolean {
    setSubmitted(true);
    const first = firstInvalidField(allErrors);
    if (!first) return true;
    if (first === "statementTimeoutMs" || first === "maxCost") setAdvancedOpen(true);
    // Wait a frame so a just-opened Advanced section is in the DOM.
    requestAnimationFrame(() => fieldRefs.current[first]?.focus());
    const count = Object.keys(allErrors).length;
    announce(`${count} field${count === 1 ? " needs" : "s need"} attention.`);
    return false;
  }

  function onParsed(parsed: ParsedConnectionUrl) {
    const { values: next, filled } = applyParsedUrl(values, parsed);
    setValues(next);
    setTestResult(null);
    announce([`Filled ${filled.join(", ")} from the URL.`, ...(parsed.notes ?? [])].join(" "));
    if (!next.env) requestAnimationFrame(() => fieldRefs.current.env?.focus());
  }

  function finishTest(result: TestResult) {
    setTestResult(result);
    announce(summarizeTest(result));
  }

  function failTest(error: ApiError) {
    setFormError(`Could not run the test. ${saveErrorMessage(error)}`);
  }

  function onTest() {
    setFormError(null);
    if (!ensureValid()) return;
    const input = toProfileInput(values);
    const savedPassword = !values.password && profile?.hasPassword;
    const savedPassphrase = !values.sshPassphrase && input.ssh?.keyFile && profile?.ssh?.hasPassphrase;
    if (profile && (savedPassword || savedPassphrase)) {
      // The unsaved-test endpoint needs the secrets; stored ones are only usable for the saved profile.
      if (connectionUnchanged(profile, values)) {
        testSaved.mutate(profile.id, { onSuccess: finishTest, onError: failTest });
      } else {
        const secret = savedPassword ? "password" : "key passphrase";
        setFormError(`Enter the ${secret} to test the changed details, or save first and test from the workbench.`);
      }
      return;
    }
    testUnsaved.mutate(input, { onSuccess: finishTest, onError: failTest });
  }

  function onSubmit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setFormError(null);
    if (saving || !ensureValid()) return;
    const input = toProfileInput(values);
    const handlers = {
      onSuccess: (saved: Profile) => {
        announce(`Saved connection ${saved.name}.`);
        onSaved(saved);
      },
      onError: (error: ApiError) => {
        if (error.code === "conflict") {
          setNameConflict(input.name);
          requestAnimationFrame(() => fieldRefs.current.name?.focus());
          return;
        }
        setFormError(saveErrorMessage(error));
      },
    };
    if (profile) update.mutate({ id: profile.id, input }, handlers);
    else create.mutate(input, handlers);
  }

  return (
    <form noValidate onSubmit={onSubmit} className="flex flex-col gap-5" aria-label="Connection details">
      <div className="flex flex-col gap-1">
        <span id={engineLabelId} className="text-xs font-medium">
          Database type
        </span>
        <SegmentedControl
          options={ENGINE_OPTIONS}
          value={values.engine}
          onValueChange={(engine) => change("engine", engine)}
          aria-labelledby={engineLabelId}
          aria-describedby={engineDescriptionId}
          className="grid h-auto w-full grid-cols-3"
        />
        <p id={engineDescriptionId} className="text-xs text-text-muted">
          {ENGINE_META[values.engine].description}
        </p>
      </div>

      {ENGINE_META[values.engine].urlExample && (
        <>
          <ConnectionUrlField
            onParsed={onParsed}
            placeholder={ENGINE_META[values.engine].urlExample ?? ""}
            inputRef={urlRef}
          />
          <div className="flex items-center gap-3 text-2xs font-medium tracking-wide text-text-muted uppercase">
            <span className="h-px flex-1 bg-border-default" />
            or enter the details
            <span className="h-px flex-1 bg-border-default" />
          </div>
        </>
      )}

      <ConnectionFields
        values={values}
        errorFor={visibleError}
        onChange={change}
        onBlur={blur}
        register={register}
        hasSavedPassword={Boolean(profile?.hasPassword)}
      />

      {sshCapable(values.engine) && (
        <SshFields
          values={values}
          errorFor={visibleError}
          onChange={change}
          onBlur={blur}
          register={register}
          hasSavedPassphrase={Boolean(profile?.ssh?.hasPassphrase)}
        />
      )}

      <AdvancedFields
        values={values}
        errors={{ statementTimeoutMs: visibleError("statementTimeoutMs"), maxCost: visibleError("maxCost") }}
        onChange={change}
        onBlur={blur}
        open={advancedOpen}
        onOpenChange={setAdvancedOpen}
        timeoutRef={register("statementTimeoutMs")}
        maxCostRef={register("maxCost")}
      />

      {testResult && <TestResultChecklist result={testResult} env={values.env} />}

      {formError && (
        <p role="alert" className="flex items-start gap-2 text-sm text-status-danger-fg">
          <CircleAlert aria-hidden className="mt-0.5 size-4 shrink-0" />
          {formError}
        </p>
      )}

      <div className="flex items-center justify-end gap-2 border-t border-border-default pt-4">
        {onCancel && (
          <Button type="button" variant="ghost" onClick={onCancel}>
            Cancel
          </Button>
        )}
        <Button type="button" variant="outline" onClick={onTest} disabled={testing}>
          {testing ? <LoaderCircle aria-hidden className="animate-spin" /> : <PlugZap aria-hidden />}
          {testing ? "Testing…" : "Test connection"}
        </Button>
        <Button type="submit" disabled={saving}>
          {saving ? "Saving…" : submitLabel}
        </Button>
      </div>
    </form>
  );
}

function connectionUnchanged(profile: Profile, values: ConnectionFormValues): boolean {
  const original = formFromProfile(profile);
  return [...CONNECTION_FIELDS].every((field) => original[field] === values[field]);
}
