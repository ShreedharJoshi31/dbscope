import { FlaskConical } from "lucide-react";
import { openSampleWorkspace } from "@/entities/schema";
import { announce } from "@/shared/lib/announcer";
import { Button } from "@/shared/ui/button";

export function openSample(): void {
  openSampleWorkspace();
  announce("Opened the sample schema.");
}

export function OpenSampleButton({ variant = "outline" }: { variant?: "outline" | "ghost" | "link" }) {
  return (
    <Button variant={variant} size="sm" onClick={openSample}>
      <FlaskConical aria-hidden />
      Open sample schema
    </Button>
  );
}
