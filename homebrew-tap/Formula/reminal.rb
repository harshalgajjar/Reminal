class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "0.7.7"
  license "MIT"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.7.7/reminal_0.7.7_darwin_arm64.tar.gz"
      sha256 "512997badf9632163a4dd77d2c1f231193fa3d64186a8f095f7f8d174fff0117"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.7.7/reminal_0.7.7_darwin_amd64.tar.gz"
      sha256 "f2943601da8f22f9777aa62d289ef668b69178bb47b5f4aceb0d84f2ab7bebd1"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.7.7/reminal_0.7.7_linux_arm64.tar.gz"
      sha256 "de3ca7598f8e27e200e5c11788f4b7ea7d7fce6cc36f7c2faf8625a7b14bfb50"
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
      "-X github.com/reminal/reminal/internal/config.DefaultCloudRelay=wss://reminal-relay.futuristic.workers.dev/ws " \
      "-X github.com/reminal/reminal/internal/config.DefaultCloudWeb=https://reminal-relay.futuristic.workers.dev"
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
