import type { ChangeEvent } from "react";
import type { ConnectionField, ConnectionFormValues } from "@/features/add-connection/model/form";
import { EnvField } from "@/features/add-connection/ui/env-field";
import type { SSLMode } from "@/shared/api";
import { FormField } from "@/shared/ui/form-field";
import { Input } from "@/shared/ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/shared/ui/select";

const SSL_OPTIONS: { value: SSLMode; label: string }[] = [
  { value: "disable", label: "disable" },
  { value: "prefer", label: "prefer (default)" },
  { value: "require", label: "require" },
  { value: "verify-ca", label: "verify-ca" },
  { value: "verify-full", label: "verify-full" },
];

interface ConnectionFieldsProps {
  values: ConnectionFormValues;
  errorFor: (field: ConnectionField) => string | undefined;
  onChange: <K extends ConnectionField>(field: K, value: ConnectionFormValues[K]) => void;
  onBlur: (field: ConnectionField) => void;
  register: (field: ConnectionField) => (el: HTMLElement | null) => void;
  hasSavedPassword: boolean;
}

/** Name, environment and the libpq connection parameters, or the file of a SQLite database. */
export function ConnectionFields({
  values,
  errorFor,
  onChange,
  onBlur,
  register,
  hasSavedPassword,
}: ConnectionFieldsProps) {
  const text = (field: "name" | "host" | "port" | "database" | "user") => ({
    ref: register(field),
    value: values[field],
    onChange: (e: ChangeEvent<HTMLInputElement>) => onChange(field, e.target.value),
    onBlur: () => onBlur(field),
  });
  const showRootCert = values.sslMode === "verify-ca" || values.sslMode === "verify-full";
  const sqlite = values.engine === "sqlite";

  return (
    <div className="grid grid-cols-6 gap-x-3 gap-y-4">
      <FormField label="Name" error={errorFor("name")} className="col-span-6">
        {(control) => <Input {...control} {...text("name")} placeholder="orders-dev" autoComplete="off" />}
      </FormField>

      <div className="col-span-6">
        <EnvField
          value={values.env}
          host={values.host}
          error={errorFor("env")}
          onChange={(env) => onChange("env", env)}
          firstRadioRef={register("env")}
        />
      </div>

      {sqlite ? (
        <FormField
          label="Database file"
          error={errorFor("path")}
          className="col-span-6"
          description="The full path on this machine. dbscope opens it read-only and never writes it; like any SQLite reader, it may add -wal and -shm files next to a WAL database."
        >
          {(control) => (
            <Input
              {...control}
              ref={register("path")}
              value={values.path}
              onChange={(e) => onChange("path", e.target.value)}
              onBlur={() => onBlur("path")}
              placeholder="/Users/you/app.db"
              autoComplete="off"
              spellCheck={false}
              className="font-mono"
            />
          )}
        </FormField>
      ) : (
        <NetworkFields
          values={values}
          errorFor={errorFor}
          onChange={onChange}
          register={register}
          hasSavedPassword={hasSavedPassword}
          text={text}
          showRootCert={showRootCert}
        />
      )}
    </div>
  );
}

function NetworkFields({
  values,
  errorFor,
  onChange,
  register,
  hasSavedPassword,
  text,
  showRootCert,
}: Omit<ConnectionFieldsProps, "onBlur"> & {
  text: (field: "host" | "port" | "database" | "user") => {
    ref: (el: HTMLElement | null) => void;
    value: string;
    onChange: (e: ChangeEvent<HTMLInputElement>) => void;
    onBlur: () => void;
  };
  showRootCert: boolean;
}) {
  return (
    <>
      <FormField label="Host" error={errorFor("host")} className="col-span-4">
        {(control) => (
          <Input
            {...control}
            {...text("host")}
            placeholder="localhost"
            autoComplete="off"
            spellCheck={false}
            className="font-mono"
          />
        )}
      </FormField>
      <FormField label="Port" error={errorFor("port")} className="col-span-2">
        {(control) => <Input {...control} {...text("port")} inputMode="numeric" className="font-mono tabular-nums" />}
      </FormField>

      <FormField label="Database" error={errorFor("database")} className="col-span-3">
        {(control) => (
          <Input
            {...control}
            {...text("database")}
            placeholder={values.engine === "mysql" ? "app" : "postgres"}
            autoComplete="off"
            spellCheck={false}
            className="font-mono"
          />
        )}
      </FormField>
      <FormField label="User" error={errorFor("user")} className="col-span-3">
        {(control) => (
          <Input
            {...control}
            {...text("user")}
            placeholder={values.engine === "mysql" ? "root" : "postgres"}
            autoComplete="off"
            spellCheck={false}
            className="font-mono"
          />
        )}
      </FormField>

      <FormField
        label="Password"
        optional
        className="col-span-3"
        description={
          hasSavedPassword
            ? "Leave empty to keep the saved password."
            : "Stored in the encrypted vault on this machine."
        }
      >
        {(control) => (
          <Input
            {...control}
            ref={register("password")}
            type="password"
            value={values.password}
            onChange={(e) => onChange("password", e.target.value)}
            placeholder={hasSavedPassword ? "••••••••" : undefined}
            autoComplete="new-password"
          />
        )}
      </FormField>
      <FormField label="SSL mode" className="col-span-3">
        {(control) => (
          <Select
            items={SSL_OPTIONS}
            value={values.sslMode}
            onValueChange={(v) => {
              if (v) onChange("sslMode", v as SSLMode);
            }}
          >
            <SelectTrigger {...control} ref={register("sslMode")} className="w-full font-mono">
              <SelectValue />
            </SelectTrigger>
            <SelectContent alignItemWithTrigger={false}>
              {SSL_OPTIONS.map((option) => (
                <SelectItem key={option.value} value={option.value} className="font-mono">
                  {option.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        )}
      </FormField>

      {showRootCert && (
        <FormField
          label="Root certificate path"
          optional
          className="col-span-6"
          description="Path to the CA certificate file on this machine. Empty uses ~/.postgresql/root.crt."
        >
          {(control) => (
            <Input
              {...control}
              value={values.sslRootCert}
              onChange={(e) => onChange("sslRootCert", e.target.value)}
              placeholder="/path/to/root.crt"
              spellCheck={false}
              className="font-mono text-xs"
            />
          )}
        </FormField>
      )}
    </>
  );
}
