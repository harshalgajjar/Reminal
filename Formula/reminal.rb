class Reminal < Formula
  desc "Remote terminal access from any browser — no SSH, no port forwarding"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "0.1.1"
  license "MIT"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.1.1/reminal_0.1.1_darwin_arm64.tar.gz"
      sha256 "12a0e22195dce807a06bdef06ce58c72d6b53b8a98b80886db4d55ffa0c17913"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.1.1/reminal_0.1.1_darwin_amd64.tar.gz"
      sha256 "9fcf8ae59525e8befd4179fcb180697854b10f71543abc52c2a5452a3ae9f54c"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.1.1/reminal_0.1.1_linux_arm64.tar.gz"
      sha256 "477c985701ae2212698798a4460f288725d17b4affc46d9c172eb0234a4b492b"
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
