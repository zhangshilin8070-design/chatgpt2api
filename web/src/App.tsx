import { Toaster } from "sonner";

import { AppShell } from "@/app/app-shell";
import { AppErrorBoundary } from "@/components/app-error-boundary";

export default function App() {
  return (
    <AppErrorBoundary>
      <Toaster position="top-center" richColors offset={48} />
      <AppShell />
    </AppErrorBoundary>
  );
}
