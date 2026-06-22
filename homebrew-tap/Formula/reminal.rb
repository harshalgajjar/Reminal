class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "0.6.0"
  license "MIT"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.6.0/reminal_0.6.0_darwin_arm64.tar.gz"
      sha256 "a9ba78794edd018cece1d271006fecdd223435bdb948afdd602c29cb7102dc83"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.6.0/reminal_0.6.0_darwin_amd64.tar.gz"
      sha256 "4c8a2fec11848053b65d89959af2322b0a34b3d599d21b8ace10a06f36e096ea"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.6.0/reminal_0.6.0_linux_arm64.tar.gz"
      sha256 "ef7bf2b5501476588610fe43eac8f08768d4d20e51f5ab2695a57a3db8bd740d"
    end
  end

  depends_on "go" => :build if build.head?

  def install
    if build.head?
      system "go", "build", "-ldflags=#{ldflags}", "-o", bin/"reminal", "./cmd/reminal"
    else
      bin.install "reminal"
    end
  end

  def ldflags
    "-s -w " \
      "-X main.version=#{version} " \
      "-X github.com/reminal/reminal/internal/config.DefaultCloudRelay=wss://reminal-relay.reminal.workers.dev/ws " \
      "-X github.com/reminal/reminal/internal/config.DefaultCloudWeb=https://reminal-relay.reminal.workers.dev"
  end

  def caveats
    <<~EOS
      reminal connects to the hosted relay automatically — no setup needed.

        reminal              # share your terminal
        reminal --connect ID --pin PIN
    EOS
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/reminal version")
  end
end
