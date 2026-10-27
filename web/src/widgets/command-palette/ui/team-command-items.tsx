import { Download, Users } from "lucide-react";
import { useActiveConnection } from "@/entities/connection";
import { downloadShared, openImportShared } from "@/features/share-state";
import { CommandGroup, CommandItem } from "@/shared/ui/command";

type Run = (action: () => void) => () => void;

/** Team file: a connection's layouts, inferred FK decisions and glossary, kept in the app's repository. */
export function TeamCommandItems({ run }: { run: Run }) {
  const { profile } = useActiveConnection();
  if (!profile) return null;
  return (
    <CommandGroup heading="Team">
      <CommandItem
        value="export team file share layout glossary foreign keys"
        onSelect={run(() => void downloadShared(profile))}
      >
        <Download aria-hidden />
        Export team file for {profile.name}…
      </CommandItem>
      <CommandItem value="import team file share layout glossary foreign keys" onSelect={run(openImportShared)}>
        <Users aria-hidden />
        Import team file into {profile.name}…
      </CommandItem>
    </CommandGroup>
  );
}
