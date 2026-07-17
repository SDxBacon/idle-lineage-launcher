function InlineError({ message }: { message: string }) {
  return (
    <p
      className="mt-[22px] px-[14px] py-3 text-[13px] leading-normal text-red-200 border border-red-400/22 rounded-xl bg-red-900/16 select-text"
      role="alert"
    >
      {message}
    </p>
  );
}

export default InlineError;
