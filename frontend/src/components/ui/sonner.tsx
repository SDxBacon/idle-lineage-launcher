import { Toaster as Sonner, type ToasterProps } from 'sonner';

function Toaster({ toastOptions, ...props }: ToasterProps) {
  return (
    <Sonner
      className="toaster group"
      toastOptions={{
        ...toastOptions,
        classNames: {
          toast: 'group toast border-border bg-popover text-popover-foreground shadow-lg',
          description: 'group-[.toast]:text-muted-foreground',
          actionButton: 'group-[.toast]:bg-primary group-[.toast]:text-primary-foreground',
          cancelButton: 'group-[.toast]:bg-muted group-[.toast]:text-muted-foreground',
          ...toastOptions?.classNames,
        },
      }}
      {...props}
    />
  );
}

export { Toaster };
